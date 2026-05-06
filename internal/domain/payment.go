package domain

import (
	"time"

	"github.com/google/uuid"
)

// Payment is an immutable record of a successful repayment event.
// PRD §4.4 — append-only; no update/delete.
type Payment struct {
	ID             uuid.UUID
	LoanID         uuid.UUID
	InstallmentID  uuid.UUID
	Amount         int64
	IdempotencyKey string
	CreatedAt      time.Time
}
