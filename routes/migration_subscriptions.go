package routes

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/checkout"
	"github.com/josephalai/sentanyl/pkg/audit"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// MIG-007 subscription takeover workflow. Imported subscriptions are
// non-charging records; the owner reviews each mapped record and makes an
// explicit decision. Activation NEVER charges a stored payment method —
// Kajabi does not export billing agreements, so continuation always runs
// through a subscription-mode Checkout link the customer completes
// themselves (imported → requires_customer_action → activated via the
// signed Stripe webhook). Every decision is audited.

type migratedSubReview struct {
	pkgmodels.MigratedSubscription
	ContactEmail string `json:"contact_email,omitempty"`
	OfferTitle   string `json:"offer_title,omitempty"`
	// PaymentMethodAvailable reports whether the contact has any Stripe
	// customer identity on this tenant. Kajabi never exports payment
	// methods, so this is informational — activation still requires the
	// customer to authorize payment via Checkout.
	PaymentMethodAvailable bool `json:"payment_method_available"`
	// RequiresCustomerAuthorization is always true for Kajabi takeovers.
	RequiresCustomerAuthorization bool `json:"requires_customer_authorization"`
}

// handleMigrationSubscriptionList is the owner review surface: every mapped
// subscription with customer, offer, amount/currency/interval, proposed
// next billing date, and authorization requirements.
func handleMigrationSubscriptionList(c *gin.Context) {
	p, ok := migrationProject(c)
	if !ok {
		return
	}
	var subs []pkgmodels.MigratedSubscription
	if err := db.GetCollection(pkgmodels.MigratedSubscriptionCollection).Find(bson.M{
		"tenant_id": p.TenantID, "project_id": p.Id,
	}).Sort("_id").All(&subs); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load subscriptions"})
		return
	}
	out := make([]migratedSubReview, 0, len(subs))
	for _, s := range subs {
		row := migratedSubReview{MigratedSubscription: s, RequiresCustomerAuthorization: true}
		var u pkgmodels.User
		if s.ContactID != "" {
			if err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
				"_id": s.ContactID, "subscriber_id": p.TenantID.Hex(),
			}).One(&u); err == nil {
				row.ContactEmail = string(u.Email)
				row.PaymentMethodAvailable = u.StripeCustomerID != ""
			}
		}
		var offer pkgmodels.Offer
		if s.OfferID != "" {
			if err := db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
				"_id": s.OfferID, "tenant_id": p.TenantID,
			}).One(&offer); err == nil {
				row.OfferTitle = offer.Title
			}
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"subscriptions": out})
}

func loadMigratedSub(c *gin.Context) (*pkgmodels.MigratedSubscription, bson.ObjectId, bool) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return nil, "", false
	}
	var sub pkgmodels.MigratedSubscription
	if err := db.GetCollection(pkgmodels.MigratedSubscriptionCollection).Find(bson.M{
		"public_id": c.Param("subId"), "tenant_id": tenantID,
	}).One(&sub); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "subscription not found"})
		return nil, "", false
	}
	return &sub, tenantID, true
}

// handleMigrationSubscriptionActivate mints the customer payment journey.
// Requires deliberate confirmation; CAS on takeover_state plus a stable
// Stripe idempotency key make concurrent/retried activations converge on
// one Checkout Session (no duplicate subscriptions).
func handleMigrationSubscriptionActivate(c *gin.Context) {
	sub, tenantID, ok := loadMigratedSub(c)
	if !ok {
		return
	}
	var req struct {
		Confirm bool `json:"confirm"`
	}
	_ = c.ShouldBindJSON(&req)
	if !req.Confirm {
		c.JSON(http.StatusBadRequest, gin.H{"error": "activation requires {\"confirm\": true} — review the mapped customer, amount, and interval first"})
		return
	}
	if sub.TakeoverState == pkgmodels.MigratedSubStateRequiresAction && sub.StripeCheckoutURL != "" {
		// Idempotent re-activate: return the already-minted journey.
		c.JSON(http.StatusOK, gin.H{"state": sub.TakeoverState, "checkout_url": sub.StripeCheckoutURL})
		return
	}
	if sub.TakeoverState != pkgmodels.MigratedSubStateImported && sub.TakeoverState != pkgmodels.MigratedSubStateFailed {
		c.JSON(http.StatusConflict, gin.H{"error": "subscription is " + sub.TakeoverState})
		return
	}

	// Resolve the tenant's Stripe credentials (own key or platform+Connect).
	var tenant pkgmodels.Tenant
	if err := db.GetCollection(pkgmodels.TenantCollection).FindId(tenantID).One(&tenant); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "tenant load failed"})
		return
	}
	stripeKey := utils.DecryptSecret(tenant.StripeSecretKey)
	stripeAcct := ""
	if stripeKey == "" && tenant.StripeConnectAccountID != "" {
		stripeKey = os.Getenv("STRIPE_PLATFORM_SECRET_KEY")
		stripeAcct = tenant.StripeConnectAccountID
	}
	if stripeKey == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Stripe is not configured for this business — connect Stripe before activating subscriptions"})
		return
	}
	// Test/live boundary: a live key only works in production.
	if strings.HasPrefix(stripeKey, "sk_live") && os.Getenv("SENTANYL_ENV") != "production" {
		c.JSON(http.StatusForbidden, gin.H{"error": "live Stripe key outside production — refusing to create live billing"})
		return
	}

	var contact pkgmodels.User
	email := ""
	if err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
		"_id": sub.ContactID, "subscriber_id": tenantID.Hex(),
	}).One(&contact); err == nil {
		email = string(contact.Email)
	}
	title := "Subscription"
	var offer pkgmodels.Offer
	if err := db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
		"_id": sub.OfferID, "tenant_id": tenantID,
	}).One(&offer); err == nil && offer.Title != "" {
		title = offer.Title
	}
	if sub.AmountMinor <= 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "imported subscription has no amount — cannot build a billing journey"})
		return
	}

	// CAS-claim the record before touching Stripe so a concurrent activate
	// loses here, not at Stripe.
	if err := db.GetCollection(pkgmodels.MigratedSubscriptionCollection).Update(bson.M{
		"_id": sub.Id, "tenant_id": tenantID,
		"takeover_state": bson.M{"$in": []string{pkgmodels.MigratedSubStateImported, pkgmodels.MigratedSubStateFailed}},
	}, bson.M{"$set": bson.M{
		"takeover_state": pkgmodels.MigratedSubStateRequiresAction,
		"decided_by":     c.GetString(auth.ContextAccountUserID),
		"decided_at":     time.Now(),
		"updated_at":     time.Now(),
	}}); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "activation already in progress"})
		return
	}

	base := os.Getenv("PUBLIC_BASE_URL")
	if base == "" {
		base = "http://localhost"
	}
	result, err := checkout.CreateStripeSubscriptionCheckout(stripeKey, stripeAcct, tenantID,
		sub.PublicId, title, sub.Currency, sub.AmountMinor, sub.Interval, email,
		base+"/portal/?subscription=activated", base+"/portal/?subscription=cancelled",
		"msub-activate-"+sub.PublicId)
	if err != nil {
		log.Printf("migration: subscription checkout for %s failed: %v", sub.PublicId, err)
		_ = db.GetCollection(pkgmodels.MigratedSubscriptionCollection).UpdateId(sub.Id, bson.M{"$set": bson.M{
			"takeover_state": pkgmodels.MigratedSubStateFailed,
			"reason":         err.Error(),
			"updated_at":     time.Now(),
		}})
		auditSubDecision(c, sub, "migration.subscription.activate", "failure", err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to create the customer billing journey"})
		return
	}
	_ = db.GetCollection(pkgmodels.MigratedSubscriptionCollection).UpdateId(sub.Id, bson.M{"$set": bson.M{
		"stripe_checkout_session_id": result.SessionID,
		"stripe_checkout_url":        result.URL,
		"reason":                     "",
		"updated_at":                 time.Now(),
	}})
	auditSubDecision(c, sub, "migration.subscription.activate", "success",
		fmt.Sprintf("checkout session %s minted; awaiting customer authorization", result.SessionID))
	c.JSON(http.StatusOK, gin.H{
		"state":        pkgmodels.MigratedSubStateRequiresAction,
		"checkout_url": result.URL,
		"note":         "send this link to the customer — billing starts only after they authorize payment",
	})
}

// handleMigrationSubscriptionDecline marks the record as not-continuing.
func handleMigrationSubscriptionDecline(c *gin.Context) {
	sub, tenantID, ok := loadMigratedSub(c)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&req)
	if err := db.GetCollection(pkgmodels.MigratedSubscriptionCollection).Update(bson.M{
		"_id": sub.Id, "tenant_id": tenantID,
		"takeover_state": bson.M{"$in": []string{pkgmodels.MigratedSubStateImported, pkgmodels.MigratedSubStateFailed, pkgmodels.MigratedSubStateRequiresAction}},
	}, bson.M{"$set": bson.M{
		"takeover_state": pkgmodels.MigratedSubStateDeclined,
		"decided_by":     c.GetString(auth.ContextAccountUserID),
		"reason":         req.Reason,
		"decided_at":     time.Now(),
		"updated_at":     time.Now(),
	}}); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "subscription is " + sub.TakeoverState})
		return
	}
	auditSubDecision(c, sub, "migration.subscription.decline", "success", req.Reason)
	c.JSON(http.StatusOK, gin.H{"state": pkgmodels.MigratedSubStateDeclined})
}

func auditSubDecision(c *gin.Context, sub *pkgmodels.MigratedSubscription, action, outcome, reason string) {
	e := audit.FromContext(c)
	e.Action, e.Outcome, e.Reason = action, outcome, reason
	e.TargetType, e.TargetID = "migrated_subscription", sub.PublicId
	audit.Record(e)
}

// SettleMigratedSubscription flips requires_customer_action → activated when
// the signed Stripe webhook reports the checkout completed. Returns true if
// a record was settled.
func SettleMigratedSubscription(tenantID bson.ObjectId, migratedSubPublicID, stripeSubscriptionID string) bool {
	now := time.Now()
	err := db.GetCollection(pkgmodels.MigratedSubscriptionCollection).Update(bson.M{
		"public_id":      migratedSubPublicID,
		"tenant_id":      tenantID,
		"takeover_state": pkgmodels.MigratedSubStateRequiresAction,
	}, bson.M{"$set": bson.M{
		"takeover_state":         pkgmodels.MigratedSubStateActivated,
		"stripe_subscription_id": stripeSubscriptionID,
		"activated_at":           now,
		"updated_at":             now,
	}})
	if err != nil {
		return false
	}
	audit.Record(audit.Event{
		TenantID: tenantID, ActorKind: "service", ActorID: "stripe-webhook",
		Action: "migration.subscription.activated", Outcome: "success",
		TargetType: "migrated_subscription", TargetID: migratedSubPublicID,
		Meta: bson.M{"stripe_subscription_id": stripeSubscriptionID},
	})
	return true
}
