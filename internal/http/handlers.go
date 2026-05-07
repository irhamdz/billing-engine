package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/irhamdz/billing-engine/internal/domain"
	"github.com/irhamdz/billing-engine/internal/service"
)

const dateFmt = "2006-01-02"

// Handler is the HTTP API surface; see PRD section 5.1.
type Handler struct {
	svc *service.BillingService
}

// NewHandler wires the billing service into HTTP handlers.
func NewHandler(svc *service.BillingService) *Handler {
	return &Handler{svc: svc}
}

// CreateLoan POST /v1/loans.
func (h *Handler) CreateLoan(w http.ResponseWriter, r *http.Request) {
	var req CreateLoanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, "INVALID_REQUEST", "malformed json")
		return
	}
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		writeError(w, err)
		return
	}
	startDate, err := time.ParseInLocation(dateFmt, req.StartDate, loc)
	if err != nil {
		writeBadRequest(w, "INVALID_REQUEST", "start_date must be YYYY-MM-DD")
		return
	}

	var borrowerID uuid.UUID
	if req.BorrowerID != "" {
		borrowerID, err = uuid.Parse(req.BorrowerID)
		if err != nil {
			writeBadRequest(w, "INVALID_REQUEST", "borrower_id must be a uuid")
			return
		}
	} else {
		borrowerID = uuid.New() // accept anonymous borrower
	}

	loan, err := h.svc.CreateLoan(r.Context(), service.CreateLoanRequest{
		BorrowerID:     borrowerID,
		Principal:      req.Principal,
		Rate:           req.AnnualInterestRate,
		TermWeeks:      req.TermWeeks,
		StartDate:      startDate,
		IdempotencyKey: idempotencyKey(r.Context()),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, CreateLoanResponse{
		LoanID:       loan.ID.String(),
		BorrowerID:   loan.BorrowerID.String(),
		TotalAmount:  loan.TotalAmount,
		WeeklyAmount: loan.WeeklyAmount,
		Status:       string(loan.Status),
		StartDate:    loan.StartDate.Format(dateFmt),
		CreatedAt:    loan.CreatedAt,
	})
}

// GetLoanSummary GET /v1/loans/{id}.
func (h *Handler) GetLoanSummary(w http.ResponseWriter, r *http.Request) {
	id, ok := parseLoanID(w, r)
	if !ok {
		return
	}
	sum, err := h.svc.GetLoanSummary(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	resp := LoanSummaryResponse{
		LoanID:         sum.Loan.ID.String(),
		BorrowerID:     sum.Loan.BorrowerID.String(),
		Status:         string(sum.Loan.Status),
		Outstanding:    sum.Outstanding,
		WeeklyAmount:   sum.Loan.WeeklyAmount,
		TotalAmount:    sum.Loan.TotalAmount,
		IsDelinquent:   sum.IsDelinquent,
		PaidCount:      sum.PaidCount,
		RemainingCount: sum.RemainingCount,
	}
	if sum.NextDueDate != nil {
		s := sum.NextDueDate.Format(dateFmt)
		resp.NextDueDate = &s
		resp.NextDueAmount = sum.NextDueAmount
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetSchedule GET /v1/loans/{id}/schedule.
func (h *Handler) GetSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := parseLoanID(w, r)
	if !ok {
		return
	}
	items, err := h.svc.GetSchedule(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]ScheduleItem, 0, len(items))
	for _, it := range items {
		row := ScheduleItem{
			WeekNumber: it.WeekNumber,
			Amount:     it.Amount,
			DueDate:    it.DueDate.Format(dateFmt),
			Status:     string(it.Status),
		}
		if it.PaidAt != nil {
			row.PaidAt = it.PaidAt.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, ScheduleResponse{LoanID: id.String(), Schedule: out})
}

// GetOutstanding GET /v1/loans/{id}/outstanding.
func (h *Handler) GetOutstanding(w http.ResponseWriter, r *http.Request) {
	id, ok := parseLoanID(w, r)
	if !ok {
		return
	}
	out, err := h.svc.GetOutstanding(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, OutstandingResponse{LoanID: id.String(), Outstanding: out})
}

// GetDelinquency GET /v1/loans/{id}/delinquency?as_of=YYYY-MM-DD.
func (h *Handler) GetDelinquency(w http.ResponseWriter, r *http.Request) {
	id, ok := parseLoanID(w, r)
	if !ok {
		return
	}
	loc, _ := time.LoadLocation("Asia/Jakarta")
	asOf := time.Now().In(loc)
	if q := r.URL.Query().Get("as_of"); q != "" {
		t, err := time.ParseInLocation(dateFmt, q, loc)
		if err != nil {
			writeBadRequest(w, "INVALID_REQUEST", "as_of must be YYYY-MM-DD")
			return
		}
		// Treat as_of as end-of-day to make "asOf the same day as a due date"
		// match common user intent.
		asOf = t.Add(24*time.Hour - time.Second)
	}
	delinq, err := h.svc.IsDelinquentAsOf(r.Context(), id, asOf)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, DelinquencyResponse{
		LoanID:       id.String(),
		IsDelinquent: delinq,
		AsOf:         asOf.Format(dateFmt),
	})
}

// MakePayment POST /v1/loans/{id}/payments.
func (h *Handler) MakePayment(w http.ResponseWriter, r *http.Request) {
	id, ok := parseLoanID(w, r)
	if !ok {
		return
	}
	key := idempotencyKey(r.Context())
	if key == "" {
		writeBadRequest(w, "INVALID_REQUEST", "Idempotency-Key header required")
		return
	}
	var req MakePaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, "INVALID_REQUEST", "malformed json")
		return
	}
	pmt, err := h.svc.MakePayment(r.Context(), id, req.Amount, key)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, paymentToDTO(pmt))
}

// GetPaymentHistory GET /v1/loans/{id}/payments.
func (h *Handler) GetPaymentHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := parseLoanID(w, r)
	if !ok {
		return
	}
	pays, err := h.svc.GetPaymentHistory(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]PaymentResponse, 0, len(pays))
	for i := range pays {
		out = append(out, paymentToDTO(&pays[i]))
	}
	writeJSON(w, http.StatusOK, PaymentHistoryResponse{LoanID: id.String(), Payments: out})
}

// helpers ------------------------------------------------------------

func parseLoanID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	raw := chi.URLParam(r, "loan_id")
	id, err := uuid.Parse(raw)
	if err != nil {
		writeBadRequest(w, "INVALID_REQUEST", "loan_id must be a uuid")
		return uuid.Nil, false
	}
	// 404 mapping when service can't find the loan happens later via writeError.
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Already wrote status; just log via stderr-like channel.
		_ = err
	}
}

func writeBadRequest(w http.ResponseWriter, code, msg string) {
	writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{Code: code, Message: msg}})
}

func paymentToDTO(p *domain.Payment) PaymentResponse {
	return PaymentResponse{
		PaymentID:      p.ID.String(),
		LoanID:         p.LoanID.String(),
		InstallmentID:  p.InstallmentID.String(),
		Amount:         p.Amount,
		IdempotencyKey: p.IdempotencyKey,
		CreatedAt:      p.CreatedAt,
	}
}

