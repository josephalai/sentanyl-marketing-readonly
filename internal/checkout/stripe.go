// Package checkout owns the Stripe Checkout Session creation helper used
// by both the public buy-now flow and the customer-side newsletter
// upgrade flow. It lives here (not in handlers) so the routes package can
// call it without creating an import cycle through handlers.
package checkout

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"gopkg.in/mgo.v2/bson"

	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// CreateStripeCheckoutSession creates a Stripe Checkout Session via the
// REST API. Metadata (tenant_id, offer_id, domain) is attached to the
// session and mirrored onto the PaymentIntent so the Stripe webhook can
// route the resulting charge back to the tenant context. Back-compat
// wrapper around CreateStripeCheckoutSessionWithExtras.
func CreateStripeCheckoutSession(stripeKey, stripeAccount string, offer *pkgmodels.Offer, tenantID bson.ObjectId, successURL, cancelURL, domain, customerEmail string) (string, error) {
	return CreateStripeCheckoutSessionWithExtras(stripeKey, stripeAccount, offer, tenantID, successURL, cancelURL, domain, customerEmail, nil)
}

// CreateStripeCheckoutSessionWithExtras is the canonical creator. Phase 11A
// Step 3 introduces the extraMetadata map so the buy-now flow can carry a
// video_session_id from the page through Stripe and onto the resulting
// PurchaseLog. Each entry is stamped on BOTH the session and the
// payment_intent so the webhook handler can read it whether it sees the
// session.completed or charge.succeeded event.
func CreateStripeCheckoutSessionWithExtras(stripeKey, stripeAccount string, offer *pkgmodels.Offer, tenantID bson.ObjectId, successURL, cancelURL, domain, customerEmail string, extraMetadata map[string]string) (string, error) {
	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("line_items[0][quantity]", "1")
	if offer.StripePriceID != "" {
		form.Set("line_items[0][price]", offer.StripePriceID)
	} else {
		form.Set("line_items[0][price_data][currency]", offer.Currency)
		form.Set("line_items[0][price_data][unit_amount]", fmt.Sprintf("%d", offer.Amount))
		form.Set("line_items[0][price_data][product_data][name]", offer.Title)
	}

	scheme := "https"
	if strings.Contains(domain, "lvh.me") || strings.Contains(domain, "localhost") {
		scheme = "http"
	}
	absSuccess := successURL
	if !strings.HasPrefix(absSuccess, "http") {
		absSuccess = scheme + "://" + domain + successURL
	}
	absCancel := cancelURL
	if !strings.HasPrefix(absCancel, "http") {
		absCancel = scheme + "://" + domain + cancelURL
	}
	form.Set("success_url", absSuccess)
	form.Set("cancel_url", absCancel)

	if customerEmail != "" {
		form.Set("customer_email", customerEmail)
	}

	form.Set("metadata[tenant_id]", tenantID.Hex())
	form.Set("metadata[offer_id]", offer.Id.Hex())
	form.Set("metadata[domain]", domain)
	form.Set("payment_intent_data[metadata][tenant_id]", tenantID.Hex())
	form.Set("payment_intent_data[metadata][offer_id]", offer.Id.Hex())
	form.Set("payment_intent_data[metadata][domain]", domain)
	for k, v := range extraMetadata {
		if k == "" || v == "" {
			continue
		}
		form.Set("metadata["+k+"]", v)
		form.Set("payment_intent_data[metadata]["+k+"]", v)
	}

	httpReq, err := http.NewRequest("POST", "https://api.stripe.com/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.SetBasicAuth(stripeKey, "")
	if stripeAccount != "" {
		httpReq.Header.Set("Stripe-Account", stripeAccount)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("stripe API request failed: %w", err)
	}
	defer resp.Body.Close()

	var stripeResp struct {
		URL   string `json:"url"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stripeResp); err != nil {
		return "", fmt.Errorf("failed to decode stripe response: %w", err)
	}
	if stripeResp.Error != nil {
		return "", fmt.Errorf("stripe error: %s", stripeResp.Error.Message)
	}
	if stripeResp.URL == "" {
		return "", fmt.Errorf("stripe returned no checkout URL")
	}
	return stripeResp.URL, nil
}
