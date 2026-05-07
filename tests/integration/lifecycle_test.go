package integration

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	httpapi "github.com/irhamdz/billing-engine/internal/http"
	"github.com/irhamdz/billing-engine/internal/repository/sqlite"
	"github.com/irhamdz/billing-engine/internal/service"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.OpenDB(context.Background(), filepath.Join(dir, "i.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	loanRepo := sqlite.NewLoanRepository(db)
	pmtRepo := sqlite.NewPaymentRepository(db)
	svc := service.NewBillingService(db, loanRepo, pmtRepo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpapi.NewRouterWithLogger(svc, logger))
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, srv *httptest.Server, path, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func get(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func bodyString(t *testing.T, r *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return string(b)
}

const createPayload = `{
	"borrower_id":"11111111-1111-1111-1111-111111111111",
	"principal":5000000,
	"annual_interest_rate":0.10,
	"term_weeks":50,
	"start_date":"2026-05-06"
}`

// extract loan_id from a known-good create response.
func mustExtractLoanID(t *testing.T, resp *http.Response) string {
	t.Helper()
	body := bodyString(t, resp)
	const k = `"loan_id":"`
	i := strings.Index(body, k)
	if i < 0 {
		t.Fatalf("no loan_id in body: %s", body)
	}
	rest := body[i+len(k):]
	j := strings.Index(rest, `"`)
	return rest[:j]
}

// PRD section 9.2 — full lifecycle.
func TestE2E_FullLifecycle(t *testing.T) {
	srv := newServer(t)
	resp := post(t, srv, "/v1/loans", createPayload, map[string]string{"Idempotency-Key": "init"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, bodyString(t, resp))
	}
	id := mustExtractLoanID(t, resp)

	for i := 0; i < 50; i++ {
		key := "lc-" + uuid.NewString()
		r := post(t, srv, "/v1/loans/"+id+"/payments", `{"amount":110000}`,
			map[string]string{"Idempotency-Key": key})
		if r.StatusCode != http.StatusCreated {
			t.Fatalf("week %d: %d %s", i, r.StatusCode, bodyString(t, r))
		}
		bodyString(t, r)
	}

	out := get(t, srv, "/v1/loans/"+id+"/outstanding")
	if !strings.Contains(bodyString(t, out), `"outstanding":0`) {
		t.Fatalf("outstanding != 0")
	}

	rej := post(t, srv, "/v1/loans/"+id+"/payments", `{"amount":110000}`,
		map[string]string{"Idempotency-Key": "post-close"})
	if rej.StatusCode != http.StatusConflict {
		t.Fatalf("post-close status=%d", rej.StatusCode)
	}
}

// PRD section 7 edge case 3 — catch-up clears delinquency at the right moment.
func TestE2E_CatchUp(t *testing.T) {
	srv := newServer(t)
	resp := post(t, srv, "/v1/loans", createPayload, map[string]string{"Idempotency-Key": "init"})
	id := mustExtractLoanID(t, resp)

	// Skip ahead 5 weeks: ask delinquency as_of 2026-06-30 (≥ 7 weeks past start).
	d := get(t, srv, "/v1/loans/"+id+"/delinquency?as_of=2026-06-30")
	if !strings.Contains(bodyString(t, d), `"is_delinquent":true`) {
		t.Fatalf("expected delinquent")
	}

	// Pay 5 sequential payments — after the 4th, only 2 missed remain → still delinquent.
	for i := 0; i < 5; i++ {
		r := post(t, srv, "/v1/loans/"+id+"/payments", `{"amount":110000}`,
			map[string]string{"Idempotency-Key": uuid.NewString()})
		if r.StatusCode != http.StatusCreated {
			t.Fatalf("pay %d: %s", i, bodyString(t, r))
		}
		bodyString(t, r)
	}
	// Now 5 paid; if asOf still 2026-06-30 (week 8 due is ~2026-07-01), there
	// should be a couple of weeks past-due → still delinquent.
	d = get(t, srv, "/v1/loans/"+id+"/delinquency?as_of=2026-06-30")
	if !strings.Contains(bodyString(t, d), `"is_delinquent":true`) {
		t.Fatalf("still delinquent after partial catch-up")
	}

	// Pay 3 more (8 total): asOf 2026-06-30 → up to week 8 may be pending,
	// only 1 consecutive miss → not delinquent.
	for i := 0; i < 3; i++ {
		r := post(t, srv, "/v1/loans/"+id+"/payments", `{"amount":110000}`,
			map[string]string{"Idempotency-Key": uuid.NewString()})
		bodyString(t, r)
	}
	d = get(t, srv, "/v1/loans/"+id+"/delinquency?as_of=2026-06-30")
	if !strings.Contains(bodyString(t, d), `"is_delinquent":false`) {
		t.Fatalf("expected not-delinquent after catch-up; body=%s", bodyString(t, d))
	}
}

// PRD section 9.2 — replay attack: same key 100×, exactly one payment row.
func TestE2E_ReplayAttack(t *testing.T) {
	srv := newServer(t)
	resp := post(t, srv, "/v1/loans", createPayload, map[string]string{"Idempotency-Key": "init"})
	id := mustExtractLoanID(t, resp)

	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			r := post(t, srv, "/v1/loans/"+id+"/payments", `{"amount":110000}`,
				map[string]string{"Idempotency-Key": "replay"})
			if r.StatusCode != http.StatusCreated {
				t.Errorf("status=%d", r.StatusCode)
			}
			bodyString(t, r)
		}()
	}
	wg.Wait()

	hist := get(t, srv, "/v1/loans/"+id+"/payments")
	body := bodyString(t, hist)
	got := strings.Count(body, `"payment_id":"`)
	if got != 1 {
		t.Fatalf("payments recorded=%d want 1; body=%s", got, body)
	}
}
