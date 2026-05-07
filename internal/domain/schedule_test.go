package domain

import (
	"testing"
	"time"
)

func mustJakarta(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Fatalf("load Asia/Jakarta: %v", err)
	}
	return loc
}

// PRD section 3.2 — default product: 5,000,000 IDR × 10% × 50wk → weekly=110,000.
func TestGenerateSchedule_DefaultProduct(t *testing.T) {
	loc := mustJakarta(t)
	start := time.Date(2026, 5, 6, 0, 0, 0, 0, loc)

	weekly, total, items, err := GenerateSchedule(5_000_000, 0.10, 50, start)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if weekly != 110_000 {
		t.Fatalf("weekly=%d want 110000", weekly)
	}
	if total != 5_500_000 {
		t.Fatalf("total=%d want 5500000", total)
	}
	if len(items) != 50 {
		t.Fatalf("len(items)=%d want 50", len(items))
	}
	var sum int64
	for i, it := range items {
		if it.WeekNumber != i+1 {
			t.Fatalf("week_number[%d]=%d want %d", i, it.WeekNumber, i+1)
		}
		if it.Status != InstallmentPending {
			t.Fatalf("status[%d]=%s want PENDING", i, it.Status)
		}
		want := start.AddDate(0, 0, 7*(i+1))
		if !it.DueDate.Equal(want) {
			t.Fatalf("due[%d]=%v want %v", i, it.DueDate, want)
		}
		sum += it.Amount
	}
	if sum != total {
		t.Fatalf("Σamount=%d want %d", sum, total)
	}
}

// PRD section 3.2 — rounding remainder absorbed by final installment.
func TestGenerateSchedule_RoundingRemainder(t *testing.T) {
	loc := mustJakarta(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)

	weekly, total, items, err := GenerateSchedule(1_000_000, 0.10, 3, start)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if total != 1_100_000 {
		t.Fatalf("total=%d want 1100000", total)
	}
	if weekly != 366_666 {
		t.Fatalf("weekly=%d want 366666 (floor)", weekly)
	}
	if len(items) != 3 {
		t.Fatalf("len=%d want 3", len(items))
	}
	if items[0].Amount != 366_666 || items[1].Amount != 366_666 {
		t.Fatalf("first two=%d/%d want 366666/366666", items[0].Amount, items[1].Amount)
	}
	if items[2].Amount != 366_668 {
		t.Fatalf("last=%d want 366668 (absorbs 2-sen remainder)", items[2].Amount)
	}
	var sum int64
	for _, it := range items {
		sum += it.Amount
	}
	if sum != total {
		t.Fatalf("sum=%d want %d", sum, total)
	}
}

func TestGenerateSchedule_Validation(t *testing.T) {
	loc := mustJakarta(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)

	cases := []struct {
		name      string
		principal int64
		rate      float64
		term      int
	}{
		{"zero term", 1000, 0.10, 0},
		{"neg term", 1000, 0.10, -1},
		{"zero principal", 0, 0.10, 50},
		{"neg principal", -1, 0.10, 50},
		{"neg rate", 1000, -0.01, 50},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, err := GenerateSchedule(c.principal, c.rate, c.term, start)
			if err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

// Edge case 11: future start_date — schedule still generates.
func TestGenerateSchedule_FutureStart(t *testing.T) {
	loc := mustJakarta(t)
	start := time.Date(2099, 6, 1, 0, 0, 0, 0, loc)
	_, _, items, err := GenerateSchedule(1_000_000, 0.10, 5, start)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !items[0].DueDate.Equal(start.AddDate(0, 0, 7)) {
		t.Fatalf("due[0]=%v", items[0].DueDate)
	}
}
