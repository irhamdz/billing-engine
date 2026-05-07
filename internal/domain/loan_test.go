package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// helpers ---------------------------------------------------------------

func newDefaultLoan(t *testing.T) *Loan {
	t.Helper()
	loc := mustJakarta(t)
	start := time.Date(2026, 5, 6, 0, 0, 0, 0, loc)
	loan, err := NewLoan(uuid.New(), 5_000_000, 0.10, 50, start)
	if err != nil {
		t.Fatalf("NewLoan: %v", err)
	}
	return loan
}

func payAll(t *testing.T, loan *Loan) {
	t.Helper()
	for i := 0; i < loan.TermWeeks; i++ {
		key := "k-" + loan.Installments[i].ID.String()
		if _, _, err := loan.MakePayment(loan.Installments[i].Amount, key, time.Now().UTC()); err != nil {
			t.Fatalf("pay week %d: %v", i+1, err)
		}
	}
}

// TestNewLoan ----------------------------------------------------------

func TestNewLoan_Invariants(t *testing.T) {
	loan := newDefaultLoan(t)

	if loan.Status != LoanActive {
		t.Fatalf("status=%s want ACTIVE", loan.Status)
	}
	if loan.Version != 0 {
		t.Fatalf("version=%d want 0", loan.Version)
	}
	if loan.TotalAmount != 5_500_000 {
		t.Fatalf("total=%d", loan.TotalAmount)
	}
	if loan.WeeklyAmount != 110_000 {
		t.Fatalf("weekly=%d", loan.WeeklyAmount)
	}
	if len(loan.Installments) != 50 {
		t.Fatalf("installments=%d", len(loan.Installments))
	}
	if err := loan.checkInvariants(); err != nil {
		t.Fatalf("invariants: %v", err)
	}
}

// TestGetOutstanding --------------------------------------------------

func TestGetOutstanding(t *testing.T) {
	loan := newDefaultLoan(t)
	if got := loan.GetOutstanding(); got != 5_500_000 {
		t.Fatalf("fresh outstanding=%d want 5500000", got)
	}

	if _, _, err := loan.MakePayment(110_000, "k1", time.Now().UTC()); err != nil {
		t.Fatalf("pay: %v", err)
	}
	if got := loan.GetOutstanding(); got != 5_390_000 {
		t.Fatalf("after 1 payment=%d want 5390000", got)
	}

	// Closed loan reports 0.
	loan2 := newDefaultLoan(t)
	payAll(t, loan2)
	if loan2.Status != LoanClosed {
		t.Fatalf("status=%s want CLOSED", loan2.Status)
	}
	if got := loan2.GetOutstanding(); got != 0 {
		t.Fatalf("closed outstanding=%d want 0", got)
	}
}

// TestIsDelinquent ---------------------------------------------------

func TestIsDelinquent(t *testing.T) {
	loc := mustJakarta(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
	mk := func(t *testing.T) *Loan {
		t.Helper()
		l, err := NewLoan(uuid.New(), 1_000_000, 0.10, 10, start)
		if err != nil {
			t.Fatalf("NewLoan: %v", err)
		}
		return l
	}

	type tc struct {
		name    string
		asOf    time.Time
		paidWks int  // pay first N weeks before checking
		want    bool
	}
	cases := []tc{
		// Edge case 10: before week 1 due date — never delinquent.
		{"before any due", start, 0, false},
		// Exactly on the day of week-1 due — not yet overdue.
		{"on due date 1", start.AddDate(0, 0, 7), 0, false},
		// One day after week 1 — only 1 miss, not delinquent.
		{"1 miss", start.AddDate(0, 0, 8), 0, false},
		// One day after week 2 — 2 misses, delinquent.
		{"2 misses", start.AddDate(0, 0, 15), 0, true},
		// 3 misses still delinquent.
		{"3 misses", start.AddDate(0, 0, 22), 0, true},
		// Pay one week then check after 2 missed dates following — only 1 consecutive miss.
		// week1 paid, asOf = day after week2 → 1 consecutive miss.
		{"miss-then-pay 1", start.AddDate(0, 0, 15), 1, false},
		// week1 paid, asOf = day after week3 → 2 consecutive misses (w2,w3) → delinquent.
		{"miss-then-pay 2", start.AddDate(0, 0, 22), 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := mk(t)
			for i := 0; i < c.paidWks; i++ {
				if _, _, err := l.MakePayment(l.Installments[i].Amount, uuid.NewString(), time.Now().UTC()); err != nil {
					t.Fatalf("pay %d: %v", i, err)
				}
			}
			if got := l.IsDelinquent(c.asOf); got != c.want {
				t.Fatalf("IsDelinquent=%v want %v", got, c.want)
			}
		})
	}
}

// PRD section 7 edge case 3 — partial catch-up clears delinquency at the right moment.
func TestIsDelinquent_CatchUpScenario(t *testing.T) {
	loc := mustJakarta(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
	loan, err := NewLoan(uuid.New(), 1_000_000, 0.10, 10, start)
	if err != nil {
		t.Fatalf("%v", err)
	}
	// Borrower has missed 5 weeks. asOf is 1 day after week 5 due date.
	asOf := start.AddDate(0, 0, 7*5+1)
	if !loan.IsDelinquent(asOf) {
		t.Fatalf("should be delinquent before any payment")
	}
	// Pay 1 → still delinquent (4 consecutive misses left).
	if _, _, err := loan.MakePayment(loan.Installments[0].Amount, "p1", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if !loan.IsDelinquent(asOf) {
		t.Fatalf("after 1 pay still delinquent")
	}
	// Pay 2,3 → still delinquent (2 misses left, w4,w5).
	if _, _, err := loan.MakePayment(loan.Installments[1].Amount, "p2", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loan.MakePayment(loan.Installments[2].Amount, "p3", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if !loan.IsDelinquent(asOf) {
		t.Fatalf("after 3 pays still delinquent (w4,w5 overdue)")
	}
	// Pay 4 → only w5 overdue → not delinquent.
	if _, _, err := loan.MakePayment(loan.Installments[3].Amount, "p4", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if loan.IsDelinquent(asOf) {
		t.Fatalf("after 4 pays should NOT be delinquent")
	}
}

// TestMakePayment -----------------------------------------------------

func TestMakePayment_HappyPath(t *testing.T) {
	loan := newDefaultLoan(t)
	now := time.Now().UTC()
	pmt, closed, err := loan.MakePayment(110_000, "k1", now)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if closed {
		t.Fatalf("loan should not be closed after first payment")
	}
	if pmt.Amount != 110_000 {
		t.Fatalf("amount=%d", pmt.Amount)
	}
	if loan.Installments[0].Status != InstallmentPaid {
		t.Fatalf("installment[0] not paid")
	}
	if loan.Version != 1 {
		t.Fatalf("version=%d want 1", loan.Version)
	}
	// Invariant check: the next payment goes to week 2 (FIFO).
	pmt2, _, err := loan.MakePayment(110_000, "k2", now)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if pmt2.InstallmentID != loan.Installments[1].ID {
		t.Fatalf("FIFO violation: paid %v want %v", pmt2.InstallmentID, loan.Installments[1].ID)
	}
}

func TestMakePayment_ExactAmount(t *testing.T) {
	loan := newDefaultLoan(t)
	cases := []int64{0, -1, 109_999, 110_001, 5_500_000}
	for _, amt := range cases {
		_, _, err := loan.MakePayment(amt, uuid.NewString(), time.Now().UTC())
		if !errors.Is(err, ErrInvalidAmount) {
			t.Fatalf("amount=%d got %v want ErrInvalidAmount", amt, err)
		}
	}
}

func TestMakePayment_FinalInstallmentClosesLoan(t *testing.T) {
	loan := newDefaultLoan(t)
	for i := 0; i < 49; i++ {
		if _, _, err := loan.MakePayment(loan.Installments[i].Amount, uuid.NewString(), time.Now().UTC()); err != nil {
			t.Fatalf("week %d: %v", i, err)
		}
	}
	if loan.Status != LoanActive {
		t.Fatalf("loan should be ACTIVE before final payment")
	}
	_, closed, err := loan.MakePayment(loan.Installments[49].Amount, "final", time.Now().UTC())
	if err != nil {
		t.Fatalf("final: %v", err)
	}
	if !closed {
		t.Fatalf("expected closed=true")
	}
	if loan.Status != LoanClosed {
		t.Fatalf("status=%s", loan.Status)
	}
	if loan.ClosedAt == nil {
		t.Fatalf("closed_at not set")
	}
}

// PRD edge case 8.
func TestMakePayment_ClosedLoanRejected(t *testing.T) {
	loan := newDefaultLoan(t)
	payAll(t, loan)
	_, _, err := loan.MakePayment(110_000, "post-close", time.Now().UTC())
	if !errors.Is(err, ErrLoanClosed) {
		t.Fatalf("got %v want ErrLoanClosed", err)
	}
}

// PRD edge case 6 — same key + same body returns existing payment.
func TestMakePayment_IdempotentReplay(t *testing.T) {
	loan := newDefaultLoan(t)
	now := time.Now().UTC()
	first, _, err := loan.MakePayment(110_000, "dup", now)
	if err != nil {
		t.Fatal(err)
	}
	versionAfterFirst := loan.Version
	second, _, err := loan.MakePayment(110_000, "dup", now)
	if err != nil {
		t.Fatalf("replay err: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("replay returned different payment id")
	}
	if loan.Version != versionAfterFirst {
		t.Fatalf("version bumped on replay: %d", loan.Version)
	}
}

// PRD edge case 7 — same key + different body → conflict.
func TestMakePayment_IdempotentConflict(t *testing.T) {
	loan := newDefaultLoan(t)
	if _, _, err := loan.MakePayment(110_000, "dup", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, _, err := loan.MakePayment(220_000, "dup", time.Now().UTC())
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("got %v want ErrIdempotencyConflict", err)
	}
}

// PRD section 3.2 — loan with rounding remainder; final amount must be exact.
func TestMakePayment_RoundingFinalInstallment(t *testing.T) {
	loc := mustJakarta(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
	loan, err := NewLoan(uuid.New(), 1_000_000, 0.10, 3, start) // 366666 / 366666 / 366668
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := loan.MakePayment(366_666, "p1", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loan.MakePayment(366_666, "p2", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// Final installment is 366668 — paying 366666 must be rejected.
	if _, _, err := loan.MakePayment(366_666, "p3-wrong", time.Now().UTC()); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("wrong final amount: %v", err)
	}
	if _, _, err := loan.MakePayment(366_668, "p3", time.Now().UTC()); err != nil {
		t.Fatalf("correct final: %v", err)
	}
	if loan.Status != LoanClosed {
		t.Fatalf("status=%s want CLOSED", loan.Status)
	}
	if loan.GetOutstanding() != 0 {
		t.Fatalf("outstanding=%d want 0", loan.GetOutstanding())
	}
}

func TestNewLoan_InvalidInputs(t *testing.T) {
	loc := mustJakarta(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
	if _, err := NewLoan(uuid.Nil, 1000, 0.1, 5, start); !errors.Is(err, ErrInvalidLoanInput) {
		t.Fatalf("nil borrower: %v", err)
	}
	if _, err := NewLoan(uuid.New(), 0, 0.1, 5, start); err == nil {
		t.Fatalf("zero principal accepted")
	}
}
