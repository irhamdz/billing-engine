// Package httpapi exposes the REST API for the billing engine.
//
// All money fields are int64 minor units (sen). Dates use YYYY-MM-DD
// (Asia/Jakarta calendar). Timestamps use RFC3339 UTC.
package httpapi

import "time"

// CreateLoanRequest is the body for POST /v1/loans.
type CreateLoanRequest struct {
	BorrowerID         string  `json:"borrower_id"`
	Principal          int64   `json:"principal"`
	AnnualInterestRate float64 `json:"annual_interest_rate"`
	TermWeeks          int     `json:"term_weeks"`
	StartDate          string  `json:"start_date"` // YYYY-MM-DD
}

// CreateLoanResponse is the body for 201 Created.
type CreateLoanResponse struct {
	LoanID       string    `json:"loan_id"`
	BorrowerID   string    `json:"borrower_id"`
	TotalAmount  int64     `json:"total_amount"`
	WeeklyAmount int64     `json:"weekly_amount"`
	Status       string    `json:"status"`
	StartDate    string    `json:"start_date"`
	CreatedAt    time.Time `json:"created_at"`
}

// LoanSummaryResponse is the body for GET /v1/loans/{id}.
type LoanSummaryResponse struct {
	LoanID         string  `json:"loan_id"`
	BorrowerID     string  `json:"borrower_id"`
	Status         string  `json:"status"`
	Outstanding    int64   `json:"outstanding"`
	WeeklyAmount   int64   `json:"weekly_amount"`
	TotalAmount    int64   `json:"total_amount"`
	NextDueDate    *string `json:"next_due_date,omitempty"`
	NextDueAmount  int64   `json:"next_due_amount,omitempty"`
	IsDelinquent   bool    `json:"is_delinquent"`
	PaidCount      int     `json:"paid_count"`
	RemainingCount int     `json:"remaining_count"`
}

// OutstandingResponse is the body for GET /v1/loans/{id}/outstanding.
type OutstandingResponse struct {
	LoanID      string `json:"loan_id"`
	Outstanding int64  `json:"outstanding"`
}

// DelinquencyResponse is the body for GET /v1/loans/{id}/delinquency.
type DelinquencyResponse struct {
	LoanID       string `json:"loan_id"`
	IsDelinquent bool   `json:"is_delinquent"`
	AsOf         string `json:"as_of"`
}

// ScheduleItem is one row in GET /v1/loans/{id}/schedule.
type ScheduleItem struct {
	WeekNumber int    `json:"week_number"`
	Amount     int64  `json:"amount"`
	DueDate    string `json:"due_date"`
	Status     string `json:"status"`
	PaidAt     string `json:"paid_at,omitempty"`
}

// ScheduleResponse wraps the list.
type ScheduleResponse struct {
	LoanID   string         `json:"loan_id"`
	Schedule []ScheduleItem `json:"schedule"`
}

// MakePaymentRequest is the body for POST /v1/loans/{id}/payments.
type MakePaymentRequest struct {
	Amount int64 `json:"amount"`
}

// PaymentResponse is the body for one payment record.
type PaymentResponse struct {
	PaymentID      string    `json:"payment_id"`
	LoanID         string    `json:"loan_id"`
	InstallmentID  string    `json:"installment_id"`
	Amount         int64     `json:"amount"`
	IdempotencyKey string    `json:"idempotency_key"`
	CreatedAt      time.Time `json:"created_at"`
}

// PaymentHistoryResponse is the list returned by GET /v1/loans/{id}/payments.
type PaymentHistoryResponse struct {
	LoanID   string            `json:"loan_id"`
	Payments []PaymentResponse `json:"payments"`
}

// errorEnvelope is the consistent error body. PRD section 5.3.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
