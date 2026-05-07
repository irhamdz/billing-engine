package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// LoanStatus is the lifecycle state of a loan. PRD section 3.5 — terminal CLOSED.
type LoanStatus string

const (
	LoanActive LoanStatus = "ACTIVE"
	LoanClosed LoanStatus = "CLOSED"
)

// Loan is the aggregate root for all billing concerns. PRD section 4.2.
//
// All mutations to the schedule and payments must flow through Loan methods so
// invariants are enforced in one place.
type Loan struct {
	ID                 uuid.UUID
	BorrowerID         uuid.UUID
	Principal          int64
	AnnualInterestRate float64
	TermWeeks          int
	TotalAmount        int64
	WeeklyAmount       int64
	StartDate          time.Time
	Status             LoanStatus
	CreatedAt          time.Time
	ClosedAt           *time.Time
	Version            int
	IdempotencyKey     string

	Installments []Installment
	Payments     []Payment
}

// NewLoan constructs a Loan and its schedule atomically.
// PRD section 3.1 / section 3.2 / section 4.2.
func NewLoan(borrowerID uuid.UUID, principal int64, rate float64, term int, start time.Time) (*Loan, error) {
	if borrowerID == uuid.Nil {
		return nil, fmt.Errorf("%w: borrower_id required", ErrInvalidLoanInput)
	}
	weekly, total, items, err := GenerateSchedule(principal, rate, term, start)
	if err != nil {
		return nil, err
	}
	loan := &Loan{
		ID:                 uuid.New(),
		BorrowerID:         borrowerID,
		Principal:          principal,
		AnnualInterestRate: rate,
		TermWeeks:          term,
		TotalAmount:        total,
		WeeklyAmount:       weekly,
		StartDate:          start,
		Status:             LoanActive,
		CreatedAt:          time.Now().UTC(),
		Version:            0,
		Installments:       items,
	}
	for i := range loan.Installments {
		loan.Installments[i].LoanID = loan.ID
	}
	if err := loan.checkInvariants(); err != nil {
		return nil, err
	}
	return loan, nil
}

// GetOutstanding returns total_amount − Σpayments. Closed loans report 0
// (PRD section 3.5, edge case 9). The formula returns the same value by invariant.
func (l *Loan) GetOutstanding() int64 {
	if l.Status == LoanClosed {
		return 0
	}
	var paid int64
	for _, p := range l.Payments {
		paid += p.Amount
	}
	return l.TotalAmount - paid
}

// IsDelinquent reports whether the borrower is delinquent on this loan as of
// the given timestamp. PRD section 3.4 — ≥ 2 consecutive PENDING installments past
// their due date, scanning by week_number ascending from the oldest pending.
func (l *Loan) IsDelinquent(asOf time.Time) bool {
	consecutive := 0
	for i := range l.Installments {
		it := &l.Installments[i]
		if it.Status == InstallmentPaid {
			// A paid installment breaks the streak. We continue scanning
			// because the spec says "consecutive PENDING past due", which
			// must be a contiguous run of unpaid past-due installments.
			consecutive = 0
			continue
		}
		if it.IsOverdue(asOf) {
			consecutive++
			if consecutive >= 2 {
				return true
			}
		} else {
			// First not-yet-overdue installment ends the scan; nothing later
			// can be overdue (installments are time-ordered).
			break
		}
	}
	return false
}

// MakePayment validates and applies a single payment to the oldest PENDING
// installment. Returns the recorded Payment plus a flag indicating whether
// this payment closed the loan. PRD section 3.3 / section 8.1.
//
// Idempotency rule: if a payment already exists for (loan, key) and the
// amount matches, the existing record is returned and no state changes.
// A mismatch returns ErrIdempotencyConflict.
//
// This method is the single mutation entry point for the aggregate; the
// service layer wraps the call in a DB transaction with optimistic version
// check (PRD section 6.1).
func (l *Loan) MakePayment(amount int64, idempotencyKey string, now time.Time) (*Payment, bool, error) {
	if existing := l.findPaymentByKey(idempotencyKey); existing != nil {
		if existing.Amount != amount {
			return nil, false, ErrIdempotencyConflict
		}
		// Replay: return existing payment unchanged.
		copy := *existing
		return &copy, false, nil
	}

	if l.Status == LoanClosed {
		return nil, false, ErrLoanClosed
	}

	pending := l.firstPending()
	if pending == nil {
		// Invariant: ACTIVE loan must have a pending installment.
		return nil, false, ErrNoPendingInstallment
	}

	if amount <= 0 || amount != pending.Amount {
		return nil, false, ErrInvalidAmount
	}

	pmt := Payment{
		ID:             uuid.New(),
		LoanID:         l.ID,
		InstallmentID:  pending.ID,
		Amount:         amount,
		IdempotencyKey: idempotencyKey,
		CreatedAt:      now,
	}
	if err := pending.markPaid(pmt.ID, now); err != nil {
		return nil, false, err
	}
	l.Payments = append(l.Payments, pmt)
	l.Version++

	closed := false
	if l.allPaid() {
		l.Status = LoanClosed
		t := now
		l.ClosedAt = &t
		closed = true
	}

	if err := l.checkInvariants(); err != nil {
		return nil, false, err
	}
	return &pmt, closed, nil
}

// Helpers ------------------------------------------------------------

func (l *Loan) firstPending() *Installment {
	for i := range l.Installments {
		if l.Installments[i].Status == InstallmentPending {
			return &l.Installments[i]
		}
	}
	return nil
}

func (l *Loan) allPaid() bool {
	for i := range l.Installments {
		if l.Installments[i].Status != InstallmentPaid {
			return false
		}
	}
	return true
}

func (l *Loan) findPaymentByKey(key string) *Payment {
	if key == "" {
		return nil
	}
	for i := range l.Payments {
		if l.Payments[i].IdempotencyKey == key {
			return &l.Payments[i]
		}
	}
	return nil
}

// checkInvariants is called after every state transition; surfaces bugs early.
// PRD section 4.2 — invariants enumerated.
func (l *Loan) checkInvariants() error {
	expectedTotal := l.Principal + int64(float64(l.Principal)*l.AnnualInterestRate)
	if l.TotalAmount != expectedTotal {
		return fmt.Errorf("invariant: total_amount=%d want %d", l.TotalAmount, expectedTotal)
	}
	var sumIns int64
	for _, it := range l.Installments {
		sumIns += it.Amount
	}
	if sumIns != l.TotalAmount {
		return fmt.Errorf("invariant: Σinstallments=%d want total=%d", sumIns, l.TotalAmount)
	}
	var sumPay int64
	for _, p := range l.Payments {
		sumPay += p.Amount
	}
	if sumPay > l.TotalAmount {
		return fmt.Errorf("invariant: Σpayments=%d > total=%d", sumPay, l.TotalAmount)
	}
	if l.Status == LoanClosed && !l.allPaid() {
		return fmt.Errorf("invariant: CLOSED but not all paid")
	}
	if l.Status == LoanActive && l.allPaid() {
		return fmt.Errorf("invariant: all paid but still ACTIVE")
	}
	return nil
}
