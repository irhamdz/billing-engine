// Package repository defines the persistence contracts for the billing engine.
//
// Concrete implementations live in subpackages (e.g. ./sqlite). The service
// layer depends only on these interfaces so a v2 server-DB swap is mechanical.
package repository

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
	"github.com/irhamdz/billing-engine/internal/domain"
)

// Tx is an opaque write transaction handle. Concrete repositories may type-
// assert it to *sql.Tx; the service layer treats it as a token.
type Tx interface {
	// Commit and Rollback are exposed so callers using WithTx need not import
	// database/sql.
	Commit() error
	Rollback() error
}

// LoanRepository persists Loan aggregates.
type LoanRepository interface {
	// Create inserts a Loan and all its installments atomically.
	Create(ctx context.Context, loan *domain.Loan) error
	// GetByID returns the loan with its installments and payment list. Used
	// for read endpoints that don't require row-locking.
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Loan, error)
	// GetByIdempotencyKey returns a loan by its creation idempotency key.
	// Returns domain.ErrNotFound when no loan exists with that key.
	GetByIdempotencyKey(ctx context.Context, key string) (*domain.Loan, error)
	// GetByIDForUpdate loads the loan inside an existing write transaction.
	GetByIDForUpdate(ctx context.Context, tx Tx, id uuid.UUID) (*domain.Loan, error)
	// Save persists changes to the loan, its newly-PAID installment(s) and any
	// new payments. Performs an optimistic version check.
	Save(ctx context.Context, tx Tx, loan *domain.Loan) error
}

// PaymentRepository handles the payments table directly for idempotency
// lookups and history queries.
type PaymentRepository interface {
	GetByIdempotencyKey(ctx context.Context, loanID uuid.UUID, key string) (*domain.Payment, error)
	ListByLoan(ctx context.Context, loanID uuid.UUID) ([]domain.Payment, error)
}

// DB is the minimal *sql.DB surface the service layer needs (begin a tx).
// Defining it as an interface lets us mock at the boundary if ever needed.
type DB interface {
	BeginImmediate(ctx context.Context) (Tx, error)
	// Underlying allows handlers/main to access the raw *sql.DB for ping/health.
	Underlying() *sql.DB
}
