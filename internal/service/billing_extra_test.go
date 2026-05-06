package service_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/irhamdz/billing-engine/internal/domain"
)

func TestService_MakePayment_MissingKey(t *testing.T) {
	svc, ctx := newSvc(t)
	loan, _ := svc.CreateLoan(ctx, defaultReq())
	if _, err := svc.MakePayment(ctx, loan.ID, 110_000, ""); !errors.Is(err, domain.ErrInvalidAmount) {
		t.Fatalf("got %v want ErrInvalidAmount", err)
	}
}

func TestService_GetOutstanding_NotFound(t *testing.T) {
	svc, ctx := newSvc(t)
	if _, err := svc.GetOutstanding(ctx, uuid.New()); !errors.Is(err, domain.ErrLoanNotFound) {
		t.Fatalf("got %v want ErrLoanNotFound", err)
	}
}

func TestService_IsDelinquent_DefaultsToNow(t *testing.T) {
	svc, ctx := newSvc(t)
	loan, _ := svc.CreateLoan(ctx, defaultReq())
	// At creation no installment has come due yet; expect not delinquent.
	delinq, err := svc.IsDelinquent(ctx, loan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if delinq {
		t.Fatalf("want false for fresh loan")
	}
}

func TestService_GetPaymentHistory_NotFound(t *testing.T) {
	svc, ctx := newSvc(t)
	if _, err := svc.GetPaymentHistory(ctx, uuid.New()); !errors.Is(err, domain.ErrLoanNotFound) {
		t.Fatalf("got %v want ErrLoanNotFound", err)
	}
}

func TestService_GetLoanSummary_HappyPath(t *testing.T) {
	svc, ctx := newSvc(t)
	loan, _ := svc.CreateLoan(ctx, defaultReq())
	if _, err := svc.MakePayment(ctx, loan.ID, 110_000, "p1"); err != nil {
		t.Fatal(err)
	}
	sum, err := svc.GetLoanSummary(ctx, loan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sum.PaidCount != 1 || sum.RemainingCount != 49 {
		t.Fatalf("paid=%d remaining=%d", sum.PaidCount, sum.RemainingCount)
	}
	if sum.NextDueDate == nil {
		t.Fatalf("next_due_date should be set")
	}
	if sum.Outstanding != 5_390_000 {
		t.Fatalf("outstanding=%d", sum.Outstanding)
	}
}

func TestService_GetLoanSummary_NotFound(t *testing.T) {
	svc, ctx := newSvc(t)
	if _, err := svc.GetLoanSummary(ctx, uuid.New()); !errors.Is(err, domain.ErrLoanNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestService_GetSchedule_NotFound(t *testing.T) {
	svc, ctx := newSvc(t)
	if _, err := svc.GetSchedule(ctx, uuid.New()); !errors.Is(err, domain.ErrLoanNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestService_CreateLoan_IdempotentReplay(t *testing.T) {
	svc, ctx := newSvc(t)
	req := defaultReq()
	req.IdempotencyKey = "create-1"
	first, err := svc.CreateLoan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateLoan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("replay returned a different loan: %s vs %s", first.ID, second.ID)
	}
}

func TestService_CreateLoan_IdempotencyConflict(t *testing.T) {
	svc, ctx := newSvc(t)
	req := defaultReq()
	req.IdempotencyKey = "create-c"
	if _, err := svc.CreateLoan(ctx, req); err != nil {
		t.Fatal(err)
	}
	conflict := req
	conflict.Principal = 1_000_000
	_, err := svc.CreateLoan(ctx, conflict)
	if !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("got %v want ErrIdempotencyConflict", err)
	}
}

func TestService_CreateLoan_MissingKey(t *testing.T) {
	svc, ctx := newSvc(t)
	req := defaultReq()
	req.IdempotencyKey = ""
	if _, err := svc.CreateLoan(ctx, req); !errors.Is(err, domain.ErrInvalidLoanInput) {
		t.Fatalf("got %v want ErrInvalidLoanInput", err)
	}
}
