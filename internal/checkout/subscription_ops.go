package checkout

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// StripeSubscriptionInfo is the subset of a Stripe subscription the customer
// self-service portal needs. Parsed from the live subscription object so a
// mutation can return the fresh state to persist onto the RecurringAgreement.
type StripeSubscriptionInfo struct {
	ID                string
	Status            string
	ItemID            string
	PriceID           string
	CurrentPeriodEnd  time.Time
	CancelAtPeriodEnd bool
	Paused            bool
}

// stripeSubscriptionRaw is the wire shape we decode from Stripe.
type stripeSubscriptionRaw struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	CurrentPeriodEnd  int64  `json:"current_period_end"`
	CancelAtPeriodEnd bool   `json:"cancel_at_period_end"`
	PauseCollection   *struct {
		Behavior string `json:"behavior"`
	} `json:"pause_collection"`
	Items struct {
		Data []struct {
			ID    string `json:"id"`
			Price struct {
				ID string `json:"id"`
			} `json:"price"`
		} `json:"data"`
	} `json:"items"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (r stripeSubscriptionRaw) toInfo() StripeSubscriptionInfo {
	info := StripeSubscriptionInfo{
		ID:                r.ID,
		Status:            r.Status,
		CancelAtPeriodEnd: r.CancelAtPeriodEnd,
		Paused:            r.PauseCollection != nil,
	}
	if r.CurrentPeriodEnd > 0 {
		info.CurrentPeriodEnd = time.Unix(r.CurrentPeriodEnd, 0).UTC()
	}
	if len(r.Items.Data) > 0 {
		info.ItemID = r.Items.Data[0].ID
		info.PriceID = r.Items.Data[0].Price.ID
	}
	return info
}

// stripeSubscriptionDo issues a form-encoded request to the Stripe
// subscriptions API, forwarding the Connect Stripe-Account header, and decodes
// the returned subscription object. Mirrors the checkout.go REST style (no SDK).
func stripeSubscriptionDo(method, stripeKey, stripeAccount, path string, form url.Values) (StripeSubscriptionInfo, error) {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req, err := http.NewRequest(method, "https://api.stripe.com"+path, body)
	if err != nil {
		return StripeSubscriptionInfo{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(stripeKey, "")
	if stripeAccount != "" {
		req.Header.Set("Stripe-Account", stripeAccount)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return StripeSubscriptionInfo{}, fmt.Errorf("stripe API request failed: %w", err)
	}
	defer resp.Body.Close()

	var raw stripeSubscriptionRaw
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return StripeSubscriptionInfo{}, fmt.Errorf("failed to decode stripe response: %w", err)
	}
	if raw.Error != nil {
		return StripeSubscriptionInfo{}, fmt.Errorf("stripe error: %s", raw.Error.Message)
	}
	return raw.toInfo(), nil
}

// GetSubscription reads the live subscription state.
func GetSubscription(stripeKey, stripeAccount, subscriptionID string) (StripeSubscriptionInfo, error) {
	return stripeSubscriptionDo("GET", stripeKey, stripeAccount, "/v1/subscriptions/"+subscriptionID, nil)
}

// SetSubscriptionCancelAtPeriodEnd schedules (cancel=true) or reverses
// (cancel=false) end-of-period cancellation without changing access before the
// paid-through timestamp.
func SetSubscriptionCancelAtPeriodEnd(stripeKey, stripeAccount, subscriptionID string, cancel bool) (StripeSubscriptionInfo, error) {
	form := url.Values{}
	form.Set("cancel_at_period_end", strconv.FormatBool(cancel))
	return stripeSubscriptionDo("POST", stripeKey, stripeAccount, "/v1/subscriptions/"+subscriptionID, form)
}

// SetSubscriptionPause pauses billing collection (pause=true) or resumes it
// (pause=false). Paused subscriptions keep the plan but stop invoicing.
func SetSubscriptionPause(stripeKey, stripeAccount, subscriptionID string, pause bool) (StripeSubscriptionInfo, error) {
	form := url.Values{}
	if pause {
		form.Set("pause_collection[behavior]", "void")
	} else {
		// Empty value unsets pause_collection, resuming normal billing.
		form.Set("pause_collection", "")
	}
	return stripeSubscriptionDo("POST", stripeKey, stripeAccount, "/v1/subscriptions/"+subscriptionID, form)
}

// ChangeSubscriptionPrice swaps the subscription's single item to a new Price
// with proration — the Stripe mechanics of a plan upgrade/downgrade.
func ChangeSubscriptionPrice(stripeKey, stripeAccount, subscriptionID, itemID, newPriceID string) (StripeSubscriptionInfo, error) {
	form := url.Values{}
	form.Set("items[0][id]", itemID)
	form.Set("items[0][price]", newPriceID)
	form.Set("proration_behavior", "create_prorations")
	return stripeSubscriptionDo("POST", stripeKey, stripeAccount, "/v1/subscriptions/"+subscriptionID, form)
}
