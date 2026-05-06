package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// InstallmentStatus is the persisted lifecycle state of a single installment.
type InstallmentStatus string

const (
	InstallmentPending InstallmentStatus = "PENDING"
	InstallmentPaid    InstallmentStatus = "PAID"
)

// Installment is a single weekly payment slot inside a Loan aggregate.
// PRD §4.3 — must only be mutated through the parent Loan.
type Installment struct {
	ID         uuid.UUID
	LoanID     uuid.UUID
	WeekNumber int               // 1-indexed
	Amount     int64             // sen
	DueDate    time.Time         // calendar date in Asia/Jakarta, time portion 00:00
	Status     InstallmentStatus
	PaidAt     *time.Time
	PaymentID  *uuid.UUID
}

// IsOverdue reports whether the installment is PENDING and asOf is strictly past its DueDate.
// PRD §4.3 / Edge case 10 — equality with due_date is NOT overdue.
func (it *Installment) IsOverdue(asOf time.Time) bool {
	return it.Status == InstallmentPending && asOf.After(it.DueDate)
}

// markPaid is intentionally unexported; only the parent Loan can call it.
func (it *Installment) markPaid(paymentID uuid.UUID, paidAt time.Time) error {
	if it.Status == InstallmentPaid {
		// Once PAID, never reverts; double-pay attempt is a programmer error.
		return fmt.Errorf("installment already paid (week %d)", it.WeekNumber)
	}
	it.Status = InstallmentPaid
	it.PaidAt = &paidAt
	it.PaymentID = &paymentID
	return nil
}

// GenerateSchedule produces the deterministic installment list for a loan.
// PRD §3.2 — flat interest, weekly = floor(total/term), final installment
// absorbs any rounding remainder so that Σamount == total.
//
// The function is pure: it does not assign installment IDs that depend on
// external state (UUIDs are generated here so callers can persist immediately).
func GenerateSchedule(principal int64, rate float64, term int, start time.Time) (weekly int64, total int64, items []Installment, err error) {
	if principal <= 0 {
		return 0, 0, nil, fmt.Errorf("%w: principal must be > 0", ErrInvalidLoanInput)
	}
	if rate < 0 {
		return 0, 0, nil, fmt.Errorf("%w: rate must be >= 0", ErrInvalidLoanInput)
	}
	if term <= 0 {
		return 0, 0, nil, fmt.Errorf("%w: term_weeks must be > 0", ErrInvalidLoanInput)
	}

	// Use float math only for the interest figure; round once and stay in int64.
	totalInterest := int64(float64(principal) * rate)
	total = principal + totalInterest
	weekly = total / int64(term)
	remainder := total - weekly*int64(term)

	items = make([]Installment, term)
	for i := 0; i < term; i++ {
		amt := weekly
		if i == term-1 {
			amt += remainder
		}
		items[i] = Installment{
			ID:         uuid.New(),
			WeekNumber: i + 1,
			Amount:     amt,
			DueDate:    start.AddDate(0, 0, 7*(i+1)),
			Status:     InstallmentPending,
		}
	}
	return weekly, total, items, nil
}
