package domain

import "errors"

// Sentinel domain errors. Higher layers translate these to HTTP codes
// (see internal/http/errors.go).
var (
	ErrInvalidLoanInput     = errors.New("invalid loan input")
	ErrLoanNotFound         = errors.New("loan not found")
	ErrLoanClosed           = errors.New("loan is closed")
	ErrInvalidAmount        = errors.New("invalid payment amount")
	ErrIdempotencyConflict  = errors.New("idempotency key reused with different payload")
	ErrNoPendingInstallment = errors.New("no pending installment on active loan") // invariant violation
	ErrVersionConflict      = errors.New("optimistic lock conflict")
	ErrNotFound             = errors.New("entity not found")
)
