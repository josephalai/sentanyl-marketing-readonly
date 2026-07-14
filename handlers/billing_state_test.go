package handlers

import "testing"

// BILL-007: Stripe subscription status maps losslessly onto its access
// consequence.
func TestSubscriptionAccessState(t *testing.T) {
	cases := map[string]string{
		"active": "active", "trialing": "active",
		"past_due": "suspend", "unpaid": "suspend", "paused": "suspend", "incomplete": "suspend",
		"canceled": "revoke", "incomplete_expired": "revoke",
		"": "noop", "weird": "noop",
	}
	for status, want := range cases {
		if got := subscriptionAccessState(status); got != want {
			t.Errorf("subscriptionAccessState(%q)=%q want %q", status, got, want)
		}
	}
}
