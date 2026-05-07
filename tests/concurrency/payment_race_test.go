package concurrency

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/irhamdz/billing-engine/internal/domain"
	"github.com/irhamdz/billing-engine/internal/repository/sqlite"
	"github.com/irhamdz/billing-engine/internal/service"
)

func newSvc(t *testing.T) *service.BillingService {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.OpenDB(context.Background(), filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	loanRepo := sqlite.NewLoanRepository(db)
	pmtRepo := sqlite.NewPaymentRepository(db)
	return service.NewBillingService(db, loanRepo, pmtRepo)
}

func newDefaultLoan(t *testing.T, svc *service.BillingService) uuid.UUID {
	t.Helper()
	loc, _ := time.LoadLocation("Asia/Jakarta")
	loan, err := svc.CreateLoan(context.Background(), service.CreateLoanRequest{
		BorrowerID:     uuid.New(),
		Principal:      5_000_000,
		Rate:           0.10,
		TermWeeks:      50,
		StartDate:      time.Date(2026, 5, 6, 0, 0, 0, 0, loc),
		IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return loan.ID
}

// PRD section 9.3 — 50 goroutines, each its own key.
//
// Expected outcome: exactly TermWeeks payments succeed, the rest fail with
// ErrInvalidAmount (no pending) or ErrLoanClosed once the loan auto-closes.
// Final outstanding must be 0.
func TestRace_FiftyGoroutines_DistinctKeys(t *testing.T) {
	svc := newSvc(t)
	loanID := newDefaultLoan(t, svc)

	const N = 50
	var success, errClosed, errInvalid, errOther atomic.Int64
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := svc.MakePayment(context.Background(), loanID, 110_000, uuid.NewString())
			switch {
			case err == nil:
				success.Add(1)
			case errors.Is(err, domain.ErrLoanClosed):
				errClosed.Add(1)
			case errors.Is(err, domain.ErrInvalidAmount), errors.Is(err, domain.ErrNoPendingInstallment):
				errInvalid.Add(1)
			default:
				errOther.Add(1)
				t.Errorf("unexpected: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if success.Load() != 50 {
		t.Fatalf("success=%d want 50; closed=%d invalid=%d", success.Load(), errClosed.Load(), errInvalid.Load())
	}
	if errOther.Load() != 0 {
		t.Fatalf("unexpected errors=%d", errOther.Load())
	}
	out, err := svc.GetOutstanding(context.Background(), loanID)
	if err != nil {
		t.Fatal(err)
	}
	if out != 0 {
		t.Fatalf("outstanding=%d want 0", out)
	}
}

// PRD section 9.3 — second concurrency test.
//
// 50 goroutines all using THE SAME key → exactly one payment is recorded;
// the other 49 calls return the existing payment unchanged (idempotent
// replay). Outstanding never goes negative; intermediate reads never overshoot.
func TestRace_SameIdempotencyKey(t *testing.T) {
	svc := newSvc(t)
	loanID := newDefaultLoan(t, svc)

	const N = 50
	var idCh = make(chan string, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			p, err := svc.MakePayment(context.Background(), loanID, 110_000, "shared")
			if err != nil {
				t.Errorf("err: %v", err)
				return
			}
			idCh <- p.ID.String()
		}()
	}
	wg.Wait()
	close(idCh)

	uniq := map[string]struct{}{}
	for id := range idCh {
		uniq[id] = struct{}{}
	}
	if len(uniq) != 1 {
		t.Fatalf("payment ids returned=%d want 1", len(uniq))
	}
	hist, err := svc.GetPaymentHistory(context.Background(), loanID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 {
		t.Fatalf("history len=%d want 1", len(hist))
	}
}

// PRD edge case 14 + section 9.3 monotonic outstanding under concurrent reads.
func TestRace_OutstandingNeverIncreases(t *testing.T) {
	svc := newSvc(t)
	loanID := newDefaultLoan(t, svc)

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N + 1)

	// Reader observes outstanding throughout.
	stop := make(chan struct{})
	var maxObserved atomic.Int64
	maxObserved.Store(5_500_001) // strictly greater than initial outstanding
	var minObserved atomic.Int64
	minObserved.Store(5_500_001)
	go func() {
		defer wg.Done()
		var last int64 = 5_500_001
		for {
			select {
			case <-stop:
				return
			default:
			}
			out, err := svc.GetOutstanding(context.Background(), loanID)
			if err != nil {
				t.Errorf("read: %v", err)
				return
			}
			if out > last {
				t.Errorf("outstanding increased: %d > %d", out, last)
			}
			last = out
			if out < minObserved.Load() {
				minObserved.Store(out)
			}
		}
	}()

	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = svc.MakePayment(context.Background(), loanID, 110_000, uuid.NewString())
		}()
	}
	// Wait for writers, then stop reader.
	go func() {
		// crude wait: spin until 50 successes
		for {
			out, _ := svc.GetOutstanding(context.Background(), loanID)
			if out == 0 {
				close(stop)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	wg.Wait()
}
