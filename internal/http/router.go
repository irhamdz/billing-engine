package httpapi

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"

	"github.com/irhamdz/billing-engine/internal/service"
)

// NewRouter wires the chi router with middleware and the handler set. PRD §5.
func NewRouter(svc *service.BillingService) http.Handler {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	return NewRouterWithLogger(svc, logger)
}

// NewRouterWithLogger lets callers (tests, main) supply a custom logger.
func NewRouterWithLogger(svc *service.BillingService, logger *slog.Logger) http.Handler {
	h := NewHandler(svc)
	r := chi.NewRouter()
	r.Use(requestID)
	r.Use(idempotency)
	r.Use(recoverer(logger))
	r.Use(accessLog(logger))

	r.Route("/v1/loans", func(r chi.Router) {
		r.Post("/", h.CreateLoan)
		r.Route("/{loan_id}", func(r chi.Router) {
			r.Get("/", h.GetLoanSummary)
			r.Get("/schedule", h.GetSchedule)
			r.Get("/outstanding", h.GetOutstanding)
			r.Get("/delinquency", h.GetDelinquency)
			r.Post("/payments", h.MakePayment)
			r.Get("/payments", h.GetPaymentHistory)
		})
	})

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	return r
}
