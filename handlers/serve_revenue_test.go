package handlers

import "testing"

// ANA-001: a $100 sale that is later refunded nets to $0 — the sale is in
// gross and the refund subtracts it. (The old code reported −$100.)
func TestRollupRevenueRefundNetsToZero(t *testing.T) {
	// One currency: a single $100 purchase, now refunded → 1 row status=refunded.
	// gross_minor counts every row (the sale happened); refunded_minor counts
	// the refunded ones.
	got := rollupRevenue([]revenueGroup{
		{Currency: "usd", GrossMinor: 10000, RefundedMinor: 10000, PaidCount: 0, RefundedCount: 1},
	})
	if len(got) != 1 {
		t.Fatalf("want 1 currency, got %d", len(got))
	}
	if got[0].NetMinor != 0 {
		t.Fatalf("refunded sale must net to 0, got %d", got[0].NetMinor)
	}
}

func TestRollupRevenueMixOfPaidAndRefunded(t *testing.T) {
	// Two $100 sales, one refunded: gross 200, refunded 100, net 100.
	got := rollupRevenue([]revenueGroup{
		{Currency: "usd", GrossMinor: 20000, RefundedMinor: 10000, PaidCount: 1, RefundedCount: 1},
	})
	if got[0].NetMinor != 10000 {
		t.Fatalf("net = %d, want 10000", got[0].NetMinor)
	}
	if got[0].GrossMinor != 20000 || got[0].RefundedMinor != 10000 {
		t.Fatal("gross/refunded not preserved")
	}
}

// ANA-003: currencies are never summed together — each is its own row.
func TestRollupRevenuePerCurrencySeparation(t *testing.T) {
	got := rollupRevenue([]revenueGroup{
		{Currency: "usd", GrossMinor: 5000, RefundedMinor: 0},
		{Currency: "eur", GrossMinor: 9000, RefundedMinor: 1000},
	})
	if len(got) != 2 {
		t.Fatalf("want 2 currencies, got %d", len(got))
	}
	// Ordered by net desc: eur (8000) before usd (5000).
	if got[0].Currency != "eur" || got[0].NetMinor != 8000 {
		t.Fatalf("expected eur/8000 first, got %s/%d", got[0].Currency, got[0].NetMinor)
	}
	if got[1].Currency != "usd" || got[1].NetMinor != 5000 {
		t.Fatalf("expected usd/5000 second, got %s/%d", got[1].Currency, got[1].NetMinor)
	}
}
