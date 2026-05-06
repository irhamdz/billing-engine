package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	httpapi "github.com/irhamdz/billing-engine/internal/http"
	"github.com/irhamdz/billing-engine/internal/repository/sqlite"
	"github.com/irhamdz/billing-engine/internal/service"
)

// TestNewRouter_DefaultLogger exercises the convenience constructor that
// production code (cmd/api) calls.
func TestNewRouter_DefaultLogger(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.OpenDB(context.Background(), filepath.Join(dir, "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	loanRepo := sqlite.NewLoanRepository(db)
	pmtRepo := sqlite.NewPaymentRepository(db)
	svc := service.NewBillingService(db, loanRepo, pmtRepo)

	r := httpapi.NewRouter(svc) // covers the default-logger branch
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}
