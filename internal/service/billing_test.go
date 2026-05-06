package service_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/irhamdz/billing-engine/internal/domain"
	"github.com/irhamdz/billing-engine/internal/repository/sqlite"
	"github.com/irhamdz/billing-engine/internal/service"
)

func newSvc(t *testing.T) (*service.BillingService, context.Context) {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.OpenDB(context.Background(), filepath.Join(dir, "svc.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	loanRepo := sqlite.NewLoanRepository(db)
	pmtRepo := sqlite.NewPaymentRepository(db)
	return service.NewBillingService(db, loanRepo, pmtRepo), context.Background()
}

func defaultReq() service.CreateLoanRequest {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	return service.CreateLoanRequest{
		BorrowerID:     uuid.New(),
		Principal:      5_000_000,
		Rate:           0.10,
		TermWeeks:      50,
		StartDate:      time.Date(2026, 5, 6, 0, 0, 0, 0, loc),
		IdempotencyKey: uuid.NewString(),
	}
}

func TestService_CreateLoan(t *testing.T) {
	svc, ctx := newSvc(t)
	loan, err := svc.CreateLoan(ctx, defaultReq())
	if err != nil {
		t.Fatalf("CreateLoan: %v", err)
	}
	if loan.WeeklyAmount != 110_000 {
		t.Fatalf("weekly=%d", loan.WeeklyAmount)
	}
	if loan.Status != domain.LoanActive {
		t.Fatalf("status=%s", loan.Status)
	}
}

func TestService_CreateLoan_Validation(t *testing.T) {
	svc, ctx := newSvc(t)
	bad := defaultReq()
	bad.Principal = 0
	if _, err := svc.CreateLoan(ctx, bad); !errors.Is(err, domain.ErrInvalidLoanInput) {
		t.Fatalf("got %v want ErrInvalidLoanInput", err)
	}
}

func TestService_MakePayment_HappyPath(t *testing.T) {
	svc, ctx := newSvc(t)
	loan, _ := svc.CreateLoan(ctx, defaultReq())

	got, err := svc.MakePayment(ctx, loan.ID, 110_000, "k1")
	if err != nil {
		t.Fatalf("pay: %v", err)
	}
	if got.Amount != 110_000 {
		t.Fatalf("amount=%d", got.Amount)
	}

	out, err := svc.GetOutstanding(ctx, loan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out != 5_390_000 {
		t.Fatalf("outstanding=%d want 5_390_000", out)
	}
}

func TestService_MakePayment_LoanNotFound(t *testing.T) {
	svc, ctx := newSvc(t)
	_, err := svc.MakePayment(ctx, uuid.New(), 110_000, "k1")
	if !errors.Is(err, domain.ErrLoanNotFound) {
		t.Fatalf("got %v want ErrLoanNotFound", err)
	}
}

func TestService_MakePayment_InvalidAmount(t *testing.T) {
	svc, ctx := newSvc(t)
	loan, _ := svc.CreateLoan(ctx, defaultReq())
	if _, err := svc.MakePayment(ctx, loan.ID, 0, "k1"); !errors.Is(err, domain.ErrInvalidAmount) {
		t.Fatalf("got %v want ErrInvalidAmount", err)
	}
	if _, err := svc.MakePayment(ctx, loan.ID, 50_000, "k2"); !errors.Is(err, domain.ErrInvalidAmount) {
		t.Fatalf("got %v want ErrInvalidAmount", err)
	}
}

func TestService_MakePayment_IdempotentReplay(t *testing.T) {
	svc, ctx := newSvc(t)
	loan, _ := svc.CreateLoan(ctx, defaultReq())
	first, err := svc.MakePayment(ctx, loan.ID, 110_000, "k-rep")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.MakePayment(ctx, loan.ID, 110_000, "k-rep")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("replay returned new payment id")
	}
	pays, _ := svc.GetPaymentHistory(ctx, loan.ID)
	if len(pays) != 1 {
		t.Fatalf("history len=%d want 1", len(pays))
	}
}

func TestService_MakePayment_IdempotencyConflict(t *testing.T) {
	svc, ctx := newSvc(t)
	loan, _ := svc.CreateLoan(ctx, defaultReq())
	if _, err := svc.MakePayment(ctx, loan.ID, 110_000, "k"); err != nil {
		t.Fatal(err)
	}
	_, err := svc.MakePayment(ctx, loan.ID, 220_000, "k")
	if !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("got %v want ErrIdempotencyConflict", err)
	}
}

func TestService_MakePayment_LoanClosed(t *testing.T) {
	svc, ctx := newSvc(t)
	loan, _ := svc.CreateLoan(ctx, defaultReq())
	for i := 0; i < 50; i++ {
		if _, err := svc.MakePayment(ctx, loan.ID, 110_000, uuid.NewString()); err != nil {
			t.Fatalf("week %d: %v", i, err)
		}
	}
	out, _ := svc.GetOutstanding(ctx, loan.ID)
	if out != 0 {
		t.Fatalf("outstanding=%d want 0", out)
	}
	_, err := svc.MakePayment(ctx, loan.ID, 110_000, uuid.NewString())
	if !errors.Is(err, domain.ErrLoanClosed) {
		t.Fatalf("got %v want ErrLoanClosed", err)
	}
}

func TestService_IsDelinquent(t *testing.T) {
	svc, ctx := newSvc(t)
	req := defaultReq()
	loc, _ := time.LoadLocation("Asia/Jakarta")
	req.StartDate = time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
	loan, _ := svc.CreateLoan(ctx, req)

	// 2 weeks overdue.
	asOf := req.StartDate.AddDate(0, 0, 15)
	delinq, err := svc.IsDelinquentAsOf(ctx, loan.ID, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if !delinq {
		t.Fatalf("want delinquent")
	}
}

func TestService_GetSchedule(t *testing.T) {
	svc, ctx := newSvc(t)
	loan, _ := svc.CreateLoan(ctx, defaultReq())
	sched, err := svc.GetSchedule(ctx, loan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sched) != 50 {
		t.Fatalf("len=%d", len(sched))
	}
}
