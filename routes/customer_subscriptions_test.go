package routes

import (
	"testing"
	"time"

	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

func TestResolveChangePlanPrice(t *testing.T) {
	cases := []struct {
		name    string
		offer   *pkgmodels.Offer
		wantID  string
		wantErr bool
	}{
		{"nil offer", nil, "", true},
		{"archived", &pkgmodels.Offer{Status: "archived", StripePriceID: "price_x"}, "", true},
		{"no stripe price", &pkgmodels.Offer{Status: "published"}, "", true},
		{"ok", &pkgmodels.Offer{Status: "published", StripePriceID: "price_ok"}, "price_ok", false},
		{"blank status ok", &pkgmodels.Offer{StripePriceID: "price_legacy"}, "price_legacy", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveChangePlanPrice(tc.offer)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantID {
				t.Fatalf("priceID = %q, want %q", got, tc.wantID)
			}
		})
	}
}

func TestToCustomerSubscriptionDTO(t *testing.T) {
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	a := pkgmodels.RecurringAgreement{
		PublicId:          "sub_123",
		Status:            "active",
		CurrentPeriodEnd:  end,
		CancelAtPeriodEnd: true,
		Paused:            false,
	}
	offer := &pkgmodels.Offer{PublicId: "offer_9", Title: "Pro Plan", Amount: 4900, Currency: "usd"}

	dto := toCustomerSubscriptionDTO(a, offer)
	if dto.PublicID != "sub_123" || dto.Status != "active" {
		t.Fatalf("identity mismatch: %+v", dto)
	}
	if !dto.CancelAtPeriodEnd || dto.Paused {
		t.Fatalf("billing flags mismatch: %+v", dto)
	}
	if dto.PlanTitle != "Pro Plan" || dto.AmountMinor != 4900 || dto.Currency != "usd" || dto.OfferPublicID != "offer_9" {
		t.Fatalf("plan summary mismatch: %+v", dto)
	}
	if !dto.CurrentPeriodEnd.Equal(end) {
		t.Fatalf("period end mismatch: %v", dto.CurrentPeriodEnd)
	}

	// Missing offer join must not panic and must leave plan fields blank.
	bare := toCustomerSubscriptionDTO(a, nil)
	if bare.PlanTitle != "" || bare.OfferPublicID != "" {
		t.Fatalf("expected blank plan summary without offer, got %+v", bare)
	}
}
