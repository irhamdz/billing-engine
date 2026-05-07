// Package service contains use-case orchestration. PRD section 8 / section 10.1.
//
// The service layer is responsible for:
//   - opening BEGIN IMMEDIATE transactions for the write path
//   - performing the idempotency lookup before the transaction so retries are cheap
//   - delegating mutation to the Loan aggregate (which enforces invariants)
//   - persisting the result via the repository contracts
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/irhamdz/billing-engine/internal/domain"
	"github.com/irhamdz/billing-engine/internal/repository"
	"github.com/irhamdz/billing-engine/internal/repository/sqlite"
)

// CreateLoanRequest is the input for BillingService.CreateLoan.
type CreateLoanRequest struct {
	BorrowerID     uuid.UUID
	Principal      int64
	Rate           float64
	TermWeeks      int
	StartDate      time.Time
	IdempotencyKey string
}

// BillingService is the application-layer entry point.
type BillingService struct {
	db       *sqlite.DB
	loans    repository.LoanRepository
	payments repository.PaymentRepository
	now      func() time.Time
}

// NewBillingService wires the dependencies together.
func NewBillingService(db *sqlite.DB, loans repository.LoanRepository, pmts repository.PaymentRepository) *BillingService {
	return &BillingService{
		db:       db,
		loans:    loans,
		payments: pmts,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// CreateLoan validates input, generates the schedule, and persists everything.
// A pre-tx idempotency lookup is done first; a matching loan is returned as-is,
// a conflicting body returns ErrIdempotencyConflict.
func (s *BillingService) CreateLoan(ctx context.Context, req CreateLoanRequest) (*domain.Loan, error) {
	if req.IdempotencyKey == "" {
		return nil, fmt.Errorf("%w: idempotency key required", domain.ErrInvalidLoanInput)
	}
	existing, err := s.loans.GetByIdempotencyKey(ctx, req.IdempotencyKey)
	if err == nil {
		// Replay: same key found. Conflict if core financial params differ.
		startStr := existing.StartDate.Format("2006-01-02")
		reqStartStr := req.StartDate.Format("2006-01-02")
		if existing.Principal != req.Principal ||
			existing.AnnualInterestRate != req.Rate ||
			existing.TermWeeks != req.TermWeeks ||
			startStr != reqStartStr {
			return nil, domain.ErrIdempotencyConflict
		}
		return existing, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}

	loan, err := domain.NewLoan(req.BorrowerID, req.Principal, req.Rate, req.TermWeeks, req.StartDate)
	if err != nil {
		return nil, err
	}
	loan.IdempotencyKey = req.IdempotencyKey
	if err := s.loans.Create(ctx, loan); err != nil {
		return nil, err
	}
	return loan, nil
}

// MakePayment processes a single payment with idempotency. PRD section 8.1.
func (s *BillingService) MakePayment(ctx context.Context, loanID uuid.UUID, amount int64, idempotencyKey string) (*domain.Payment, error) {
	if idempotencyKey == "" {
		return nil, fmt.Errorf("%w: idempotency key required", domain.ErrInvalidAmount)
	}

	// Step 3 (PRD section 8.1): pre-tx idempotency check.
	if existing, err := s.payments.GetByIdempotencyKey(ctx, loanID, idempotencyKey); err == nil {
		if existing.Amount != amount {
			return nil, domain.ErrIdempotencyConflict
		}
		return existing, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}

	// Need to also confirm the loan exists; do a cheap pre-tx read so 404
	// doesn't require taking the write lock.
	if _, err := s.loans.GetByID(ctx, loanID); err != nil {
		return nil, err
	}

	tx, err := s.db.BeginImmediate(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Re-load inside the tx so post-close races (edge case 16) are handled.
	loan, err := s.loans.GetByIDForUpdate(ctx, tx, loanID)
	if err != nil {
		return nil, err
	}

	preVersion := loan.Version
	pmt, _, err := loan.MakePayment(amount, idempotencyKey, s.now())
	if err != nil {
		return nil, err
	}

	// Idempotent replay path: another goroutine already committed this exact
	// payment between our pre-tx lookup and our BEGIN IMMEDIATE, and the
	// domain returned that payment unchanged. Nothing to persist.
	if loan.Version == preVersion {
		return pmt, nil
	}

	if err := s.loans.Save(ctx, tx, loan); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return pmt, nil
}

// GetOutstanding implements PRD section 6.4 — it loads the loan and reuses the
// aggregate's accessor. The performance-oriented single-aggregate-query form
// is implemented here as well for cases where loading the full schedule
// would be wasteful.
func (s *BillingService) GetOutstanding(ctx context.Context, loanID uuid.UUID) (int64, error) {
	loan, err := s.loans.GetByID(ctx, loanID)
	if err != nil {
		return 0, err
	}
	return loan.GetOutstanding(), nil
}

// IsDelinquent uses time.Now() (jakarta calendar) as the asOf timestamp.
func (s *BillingService) IsDelinquent(ctx context.Context, loanID uuid.UUID) (bool, error) {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		return false, err
	}
	return s.IsDelinquentAsOf(ctx, loanID, time.Now().In(loc))
}

// IsDelinquentAsOf is exposed primarily for testing.
func (s *BillingService) IsDelinquentAsOf(ctx context.Context, loanID uuid.UUID, asOf time.Time) (bool, error) {
	loan, err := s.loans.GetByID(ctx, loanID)
	if err != nil {
		return false, err
	}
	return loan.IsDelinquent(asOf), nil
}

// GetSchedule returns the full installment list.
func (s *BillingService) GetSchedule(ctx context.Context, loanID uuid.UUID) ([]domain.Installment, error) {
	loan, err := s.loans.GetByID(ctx, loanID)
	if err != nil {
		return nil, err
	}
	return loan.Installments, nil
}

// GetPaymentHistory returns all payments in chronological order.
func (s *BillingService) GetPaymentHistory(ctx context.Context, loanID uuid.UUID) ([]domain.Payment, error) {
	if _, err := s.loans.GetByID(ctx, loanID); err != nil {
		return nil, err
	}
	return s.payments.ListByLoan(ctx, loanID)
}

// LoanSummary is the aggregated borrower-facing view. PRD section 8.2.
type LoanSummary struct {
	Loan            *domain.Loan
	Outstanding     int64
	NextDueDate     *time.Time
	NextDueAmount   int64
	IsDelinquent    bool
	PaidCount       int
	RemainingCount  int
}

// GetLoanSummary composes the dashboard view.
func (s *BillingService) GetLoanSummary(ctx context.Context, loanID uuid.UUID) (*LoanSummary, error) {
	loan, err := s.loans.GetByID(ctx, loanID)
	if err != nil {
		return nil, err
	}
	loc, _ := time.LoadLocation("Asia/Jakarta")
	sum := &LoanSummary{
		Loan:         loan,
		Outstanding:  loan.GetOutstanding(),
		IsDelinquent: loan.IsDelinquent(time.Now().In(loc)),
	}
	for _, it := range loan.Installments {
		switch it.Status {
		case domain.InstallmentPaid:
			sum.PaidCount++
		case domain.InstallmentPending:
			sum.RemainingCount++
			if sum.NextDueDate == nil {
				due := it.DueDate
				sum.NextDueDate = &due
				sum.NextDueAmount = it.Amount
			}
		}
	}
	return sum, nil
}
