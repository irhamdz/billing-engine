package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/irhamdz/billing-engine/internal/domain"
)

// writeError maps a domain error to the §5.3 HTTP envelope.
func writeError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")

	code, msg, status := classify(err)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{
		Error: errorBody{Code: code, Message: msg},
	})
}

func classify(err error) (code, msg string, status int) {
	switch {
	case errors.Is(err, domain.ErrLoanNotFound):
		return "LOAN_NOT_FOUND", "loan not found", http.StatusNotFound
	case errors.Is(err, domain.ErrLoanClosed):
		return "LOAN_CLOSED", "loan is already closed", http.StatusConflict
	case errors.Is(err, domain.ErrInvalidAmount):
		return "INVALID_AMOUNT", "payment amount does not match weekly amount", http.StatusBadRequest
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return "IDEMPOTENCY_CONFLICT", "idempotency key reused with a different payload", http.StatusConflict
	case errors.Is(err, domain.ErrInvalidLoanInput):
		return "INVALID_REQUEST", err.Error(), http.StatusBadRequest
	}
	return "INTERNAL_ERROR", "internal server error", http.StatusInternalServerError
}
