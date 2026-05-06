package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/irhamdz/billing-engine/internal/domain"
)

func mustJakarta(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Fatalf("load Asia/Jakarta: %v", err)
	}
	return loc
}

func newLoanForTest(t *testing.T) *domain.Loan {
	t.Helper()
	loc := mustJakarta(t)
	start := time.Date(2026, 5, 6, 0, 0, 0, 0, loc)
	loan, err := domain.NewLoan(uuid.New(), 5_000_000, 0.10, 50, start)
	if err != nil {
		t.Fatalf("NewLoan: %v", err)
	}
	return loan
}

func TestLoanRepo_CreateAndGet(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := NewLoanRepository(db)

	loan := newLoanForTest(t)
	if err := repo.Create(ctx, loan); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, loan.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != loan.ID {
		t.Fatalf("id mismatch")
	}
	if got.WeeklyAmount != 110_000 || got.TotalAmount != 5_500_000 {
		t.Fatalf("amounts: %+v", got)
	}
	if len(got.Installments) != 50 {
		t.Fatalf("installments=%d", len(got.Installments))
	}
	for i, it := range got.Installments {
		if it.WeekNumber != i+1 {
			t.Fatalf("week order broken at %d", i)
		}
		if it.Status != domain.InstallmentPending {
			t.Fatalf("status[%d]=%s", i, it.Status)
		}
	}
}

func TestLoanRepo_GetByID_NotFound(t *testing.T) {
	db := openTestDB(t)
	repo := NewLoanRepository(db)
	_, err := repo.GetByID(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrLoanNotFound) {
		t.Fatalf("got %v want ErrLoanNotFound", err)
	}
}

func TestLoanRepo_SaveAfterPayment(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	loanRepo := NewLoanRepository(db)
	pmtRepo := NewPaymentRepository(db)
	_ = pmtRepo

	loan := newLoanForTest(t)
	if err := loanRepo.Create(ctx, loan); err != nil {
		t.Fatalf("Create: %v", err)
	}

	tx, err := db.BeginImmediate(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	got, err := loanRepo.GetByIDForUpdate(ctx, tx, loan.ID)
	if err != nil {
		t.Fatalf("GetByIDForUpdate: %v", err)
	}
	if _, _, err := got.MakePayment(110_000, "k1", time.Now().UTC()); err != nil {
		t.Fatalf("MakePayment: %v", err)
	}
	if err := loanRepo.Save(ctx, tx, got); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Re-read; installment 1 should be PAID, version=1, payment recorded.
	round, err := loanRepo.GetByID(ctx, loan.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if round.Version != 1 {
		t.Fatalf("version=%d", round.Version)
	}
	if round.Installments[0].Status != domain.InstallmentPaid {
		t.Fatalf("week 1 not PAID")
	}
	pays, err := pmtRepo.ListByLoan(ctx, loan.ID)
	if err != nil {
		t.Fatalf("ListByLoan: %v", err)
	}
	if len(pays) != 1 || pays[0].Amount != 110_000 {
		t.Fatalf("payments=%+v", pays)
	}
}

func TestLoanRepo_Save_OptimisticLock(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	loanRepo := NewLoanRepository(db)

	loan := newLoanForTest(t)
	if err := loanRepo.Create(ctx, loan); err != nil {
		t.Fatal(err)
	}

	// Simulate a stale aggregate: bump version directly in DB so our
	// in-memory loan.Version no longer matches.
	if _, err := db.Underlying().ExecContext(ctx,
		`UPDATE loans SET version = 99 WHERE id = ?`, loan.ID.String()); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginImmediate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	loan.Version = 0 // stale
	loan.Status = domain.LoanClosed
	err = loanRepo.Save(ctx, tx, loan)
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("got %v want ErrVersionConflict", err)
	}
}

func TestPaymentRepo_GetByIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	loanRepo := NewLoanRepository(db)
	pmtRepo := NewPaymentRepository(db)

	loan := newLoanForTest(t)
	if err := loanRepo.Create(ctx, loan); err != nil {
		t.Fatal(err)
	}

	if _, err := pmtRepo.GetByIdempotencyKey(ctx, loan.ID, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("got %v want ErrNotFound", err)
	}

	tx, _ := db.BeginImmediate(ctx)
	got, _ := loanRepo.GetByIDForUpdate(ctx, tx, loan.ID)
	if _, _, err := got.MakePayment(110_000, "key-1", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := loanRepo.Save(ctx, tx, got); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	found, err := pmtRepo.GetByIdempotencyKey(ctx, loan.ID, "key-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if found.Amount != 110_000 || found.IdempotencyKey != "key-1" {
		t.Fatalf("payment=%+v", found)
	}
}
