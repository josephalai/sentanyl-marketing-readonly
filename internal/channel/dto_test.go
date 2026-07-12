package channel

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gopkg.in/mgo.v2/bson"

	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// TestPublicDTOsDoNotLeakInternalFields is the leak-regression guard: every
// public DTO, fully populated from a fully populated model, must serialize
// without tenant ids, internal ObjectIds, or Stripe identifiers.
func TestPublicDTOsDoNotLeakInternalFields(t *testing.T) {
	tenantID := bson.NewObjectId()
	now := time.Now()

	product := &pkgmodels.Product{
		Id:           bson.NewObjectId(),
		PublicId:     "prod_pub_1",
		TenantID:     tenantID,
		SubscriberId: "sub_123",
		Name:         "Workshop",
		Description:  "desc",
		ProductType:  "course",
		ThumbnailURL: "https://cdn/x.png",
		Status:       pkgmodels.ProductStatusActive,
		Price:        100,
		Currency:     "usd",
		StripeId:     "prod_stripe_LEAK",
	}
	offer := &pkgmodels.Offer{
		Id:               bson.NewObjectId(),
		PublicId:         "offer_pub_1",
		TenantID:         tenantID,
		SubscriberId:     "sub_123",
		Title:            "Workshop Offer",
		StripeProductID:  "prod_stripe_LEAK",
		StripePriceID:    "price_stripe_LEAK",
		PricingModel:     "one_time",
		Amount:           10000,
		Currency:         "usd",
		GrantedBadges:    []string{"workshop-owner"},
		IncludedProducts: []bson.ObjectId{product.Id},
	}
	ch := &pkgmodels.FrontendChannel{
		Id:        bson.NewObjectId(),
		PublicId:  "chan_pub_1",
		TenantID:  tenantID,
		Type:      pkgmodels.FrontendChannelTypeCodedWebsite,
		Name:      "josephalai.net",
		Status:    pkgmodels.FrontendChannelStatusActive,
		Domain:    "josephalai.net",
		PublicKey: "pub_abc",
	}
	ch.SoftDeletes.CreatedAt = &now

	payloads := map[string]interface{}{
		"product": ToPublicProduct(product),
		"offer":   ToPublicOffer(offer, map[string]string{product.Id.Hex(): product.PublicId}),
		"channel": ToPublicChannel(ch, tenantID.Hex(), "josephalai.net"),
	}

	for name, payload := range payloads {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		body := strings.ToLower(string(raw))
		// tenant_id is deliberately allowed on the channel bootstrap (the
		// video event API keys on it); it must never appear elsewhere.
		forbidden := []string{"stripe", "subscriber_id", "granted_badges", tenantID.Hex()}
		if name == "channel" {
			forbidden = []string{"stripe", "subscriber_id", "public_key"}
		}
		for _, needle := range forbidden {
			if strings.Contains(body, needle) {
				t.Errorf("%s DTO leaks %q: %s", name, needle, body)
			}
		}
		// Internal ObjectIds must not appear in any DTO.
		for _, id := range []bson.ObjectId{product.Id, offer.Id, ch.Id} {
			if strings.Contains(body, id.Hex()) {
				t.Errorf("%s DTO leaks internal ObjectId %s: %s", name, id.Hex(), body)
			}
		}
	}
}
