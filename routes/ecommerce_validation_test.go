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
