package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	httpapi "github.com/irhamdz/billing-engine/internal/http"
	"github.com/irhamdz/billing-engine/internal/repository/sqlite"
	"github.com/irhamdz/billing-engine/internal/service"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.OpenDB(context.Background(), filepath.Join(dir, "h.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	loanRepo := sqlite.NewLoanRepository(db)
	pmtRepo := sqlite.NewPaymentRepository(db)
	svc := service.NewBillingService(db, loanRepo, pmtRepo)
	silentLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpapi.NewRouterWithLogger(svc, silentLogger))
	t.Cleanup(srv.Close)
	return srv
}

func decode(t *testing.T, r io.Reader, dst any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(dst); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func doReq(t *testing.T, srv *httptest.Server, method, path, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
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

func createLoan(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	body := `{
		"borrower_id":"11111111-1111-1111-1111-111111111111",
		"principal":5000000,
		"annual_interest_rate":0.10,
		"term_weeks":50,
		"start_date":"2026-05-06"
	}`
	resp := doReq(t, srv, "POST", "/v1/loans", body, map[string]string{"Idempotency-Key": "init"})
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	var out struct {
		LoanID       string `json:"loan_id"`
		WeeklyAmount int64  `json:"weekly_amount"`
		TotalAmount  int64  `json:"total_amount"`
	}
	decode(t, resp.Body, &out)
	resp.Body.Close()
	if out.WeeklyAmount != 110_000 || out.TotalAmount != 5_500_000 {
		t.Fatalf("weekly=%d total=%d", out.WeeklyAmount, out.TotalAmount)
	}
	return out.LoanID
}

func TestHTTP_CreateLoan(t *testing.T) {
	srv := newServer(t)
	id := createLoan(t, srv)
	if len(id) == 0 {
		t.Fatal("no loan id")
	}
}

func TestHTTP_CreateLoan_BadInput(t *testing.T) {
	srv := newServer(t)
	resp := doReq(t, srv, "POST", "/v1/loans", `{"principal":-1}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestHTTP_CreateLoan_IdempotentReplay(t *testing.T) {
	srv := newServer(t)
	body := `{"borrower_id":"11111111-1111-1111-1111-111111111111","principal":5000000,"annual_interest_rate":0.10,"term_weeks":50,"start_date":"2026-05-06"}`
	headers := map[string]string{"Idempotency-Key": "loan-idem-1"}

	first := doReq(t, srv, "POST", "/v1/loans", body, headers)
	if first.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(first.Body)
		t.Fatalf("first status=%d body=%s", first.StatusCode, b)
	}
	firstBody, _ := io.ReadAll(first.Body)
	first.Body.Close()

	second := doReq(t, srv, "POST", "/v1/loans", body, headers)
	if second.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(second.Body)
		t.Fatalf("second status=%d body=%s", second.StatusCode, b)
	}
	secondBody, _ := io.ReadAll(second.Body)
	second.Body.Close()

	if !bytes.Equal(firstBody, secondBody) {
		t.Fatalf("replay body differs:\n%s\n%s", firstBody, secondBody)
	}
}

func TestHTTP_CreateLoan_IdempotencyConflict(t *testing.T) {
	srv := newServer(t)
	headers := map[string]string{"Idempotency-Key": "loan-conflict"}
	doReq(t, srv, "POST", "/v1/loans",
		`{"borrower_id":"11111111-1111-1111-1111-111111111111","principal":5000000,"annual_interest_rate":0.10,"term_weeks":50,"start_date":"2026-05-06"}`,
		headers).Body.Close()
	resp := doReq(t, srv, "POST", "/v1/loans",
		`{"borrower_id":"11111111-1111-1111-1111-111111111111","principal":1000000,"annual_interest_rate":0.10,"term_weeks":50,"start_date":"2026-05-06"}`,
		headers)
	if resp.StatusCode != http.StatusConflict {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	var errBody struct {
		Error struct{ Code string `json:"code"` } `json:"error"`
	}
	decode(t, resp.Body, &errBody)
	resp.Body.Close()
	if errBody.Error.Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("code=%s", errBody.Error.Code)
	}
}

func TestHTTP_GetOutstanding(t *testing.T) {
	srv := newServer(t)
	id := createLoan(t, srv)
	resp := doReq(t, srv, "GET", "/v1/loans/"+id+"/outstanding", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		LoanID      string `json:"loan_id"`
		Outstanding int64  `json:"outstanding"`
	}
	decode(t, resp.Body, &body)
	resp.Body.Close()
	if body.Outstanding != 5_500_000 {
		t.Fatalf("outstanding=%d", body.Outstanding)
	}
	if body.LoanID != id {
		t.Fatalf("loan_id=%s want %s", body.LoanID, id)
	}
}

func TestHTTP_GetOutstanding_NotFound(t *testing.T) {
	srv := newServer(t)
	resp := doReq(t, srv, "GET", "/v1/loans/00000000-0000-0000-0000-000000000000/outstanding", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decode(t, resp.Body, &body)
	resp.Body.Close()
	if body.Error.Code != "LOAN_NOT_FOUND" {
		t.Fatalf("code=%s", body.Error.Code)
	}
}

func TestHTTP_MakePayment_HappyPath(t *testing.T) {
	srv := newServer(t)
	id := createLoan(t, srv)
	resp := doReq(t, srv, "POST", "/v1/loans/"+id+"/payments", `{"amount":110000}`,
		map[string]string{"Idempotency-Key": "p1"})
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	var body struct {
		PaymentID string `json:"payment_id"`
		Amount    int64  `json:"amount"`
	}
	decode(t, resp.Body, &body)
	resp.Body.Close()
	if body.Amount != 110000 || body.PaymentID == "" {
		t.Fatalf("body=%+v", body)
	}
}

func TestHTTP_MakePayment_InvalidAmount(t *testing.T) {
	srv := newServer(t)
	id := createLoan(t, srv)
	resp := doReq(t, srv, "POST", "/v1/loans/"+id+"/payments", `{"amount":50000}`,
		map[string]string{"Idempotency-Key": "bad"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decode(t, resp.Body, &body)
	resp.Body.Close()
	if body.Error.Code != "INVALID_AMOUNT" {
		t.Fatalf("code=%s", body.Error.Code)
	}
}

func TestHTTP_MakePayment_IdempotentReplay(t *testing.T) {
	srv := newServer(t)
	id := createLoan(t, srv)

	first := doReq(t, srv, "POST", "/v1/loans/"+id+"/payments", `{"amount":110000}`,
		map[string]string{"Idempotency-Key": "rep"})
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first status=%d", first.StatusCode)
	}
	firstBody, _ := io.ReadAll(first.Body)
	first.Body.Close()

	second := doReq(t, srv, "POST", "/v1/loans/"+id+"/payments", `{"amount":110000}`,
		map[string]string{"Idempotency-Key": "rep"})
	if second.StatusCode != http.StatusCreated {
		t.Fatalf("second status=%d", second.StatusCode)
	}
	secondBody, _ := io.ReadAll(second.Body)
	second.Body.Close()

	if !bytes.Equal(firstBody, secondBody) {
		t.Fatalf("replay body diff:\n%s\n%s", firstBody, secondBody)
	}
}

func TestHTTP_MakePayment_IdempotencyConflict(t *testing.T) {
	srv := newServer(t)
	id := createLoan(t, srv)
	doReq(t, srv, "POST", "/v1/loans/"+id+"/payments", `{"amount":110000}`,
		map[string]string{"Idempotency-Key": "c"}).Body.Close()
	resp := doReq(t, srv, "POST", "/v1/loans/"+id+"/payments", `{"amount":220000}`,
		map[string]string{"Idempotency-Key": "c"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decode(t, resp.Body, &body)
	resp.Body.Close()
	if body.Error.Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("code=%s", body.Error.Code)
	}
}

func TestHTTP_MakePayment_LoanClosed(t *testing.T) {
	srv := newServer(t)
	id := createLoan(t, srv)
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("p%d", i)
		resp := doReq(t, srv, "POST", "/v1/loans/"+id+"/payments", `{"amount":110000}`,
			map[string]string{"Idempotency-Key": key})
		if resp.StatusCode != http.StatusCreated {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("week %d status=%d body=%s", i, resp.StatusCode, b)
		}
		resp.Body.Close()
	}
	resp := doReq(t, srv, "POST", "/v1/loans/"+id+"/payments", `{"amount":110000}`,
		map[string]string{"Idempotency-Key": "post-close"})
	if resp.StatusCode != http.StatusConflict {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decode(t, resp.Body, &body)
	resp.Body.Close()
	if body.Error.Code != "LOAN_CLOSED" {
		t.Fatalf("code=%s", body.Error.Code)
	}
}

func TestHTTP_GetSchedule(t *testing.T) {
	srv := newServer(t)
	id := createLoan(t, srv)
	resp := doReq(t, srv, "GET", "/v1/loans/"+id+"/schedule", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		Schedule []struct {
			WeekNumber int    `json:"week_number"`
			Amount     int64  `json:"amount"`
			DueDate    string `json:"due_date"`
			Status     string `json:"status"`
		} `json:"schedule"`
	}
	decode(t, resp.Body, &body)
	resp.Body.Close()
	if len(body.Schedule) != 50 {
		t.Fatalf("len=%d", len(body.Schedule))
	}
	if body.Schedule[0].Amount != 110000 || body.Schedule[0].Status != "PENDING" {
		t.Fatalf("first=%+v", body.Schedule[0])
	}
}

func TestHTTP_GetDelinquency(t *testing.T) {
	srv := newServer(t)
	id := createLoan(t, srv)
	// Without payments and asOf=2026-06-01, two installments must be overdue.
	resp := doReq(t, srv, "GET", "/v1/loans/"+id+"/delinquency?as_of=2026-06-01", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		IsDelinquent bool `json:"is_delinquent"`
	}
	decode(t, resp.Body, &body)
	resp.Body.Close()
	if !body.IsDelinquent {
		t.Fatalf("want delinquent")
	}
}

func TestHTTP_PaymentHistory(t *testing.T) {
	srv := newServer(t)
	id := createLoan(t, srv)
	doReq(t, srv, "POST", "/v1/loans/"+id+"/payments", `{"amount":110000}`,
		map[string]string{"Idempotency-Key": "h1"}).Body.Close()
	resp := doReq(t, srv, "GET", "/v1/loans/"+id+"/payments", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		Payments []struct {
			PaymentID string `json:"payment_id"`
			Amount    int64  `json:"amount"`
		} `json:"payments"`
	}
	decode(t, resp.Body, &body)
	resp.Body.Close()
	if len(body.Payments) != 1 || body.Payments[0].Amount != 110000 {
		t.Fatalf("payments=%+v", body.Payments)
	}
}

func TestHTTP_GetLoanSummary(t *testing.T) {
	srv := newServer(t)
	id := createLoan(t, srv)
	resp := doReq(t, srv, "GET", "/v1/loans/"+id, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		LoanID         string `json:"loan_id"`
		Status         string `json:"status"`
		Outstanding    int64  `json:"outstanding"`
		PaidCount      int    `json:"paid_count"`
		RemainingCount int    `json:"remaining_count"`
	}
	decode(t, resp.Body, &body)
	resp.Body.Close()
	if body.Status != "ACTIVE" || body.Outstanding != 5_500_000 || body.RemainingCount != 50 {
		t.Fatalf("body=%+v", body)
	}
}
