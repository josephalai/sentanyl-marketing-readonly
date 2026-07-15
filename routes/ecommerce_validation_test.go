package routes

import "testing"

func TestValidPricingModel(t *testing.T) {
	for _, model := range []string{"free", "one_time", "recurring", "payment_plan"} {
		if !validPricingModel(model) {
			t.Fatalf("expected pricing model %q to be accepted", model)
		}
	}
	for _, model := range []string{"", "subscription", "one-time", "ONE_TIME"} {
		if validPricingModel(model) {
			t.Fatalf("expected pricing model %q to be rejected", model)
		}
	}
}

func TestValidOfferAmount(t *testing.T) {
	tests := []struct {
		name         string
		pricingModel string
		amount       int64
		want         bool
	}{
		{name: "free zero", pricingModel: "free", amount: 0, want: true},
		{name: "free positive", pricingModel: "free", amount: 1, want: false},
		{name: "one time positive", pricingModel: "one_time", amount: 1, want: true},
		{name: "one time zero", pricingModel: "one_time", amount: 0, want: false},
		{name: "one time negative", pricingModel: "one_time", amount: -1, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validOfferAmount(tt.pricingModel, tt.amount); got != tt.want {
				t.Fatalf("validOfferAmount(%q, %d) = %v, want %v", tt.pricingModel, tt.amount, got, tt.want)
			}
		})
	}
}
