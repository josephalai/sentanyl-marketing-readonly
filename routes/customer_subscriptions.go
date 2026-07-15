package routes

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/checkout"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// RegisterCustomerSubscriptionRoutes exposes the customer-driven subscription
// self-service surface (COM: cancel / resume / pause / change-plan). Mounted on
// the /api/customer group in marketing-service/cmd/main.go, behind
// RequireCustomerAuth. Every handler is ownership-scoped to the authenticated
// contact so one customer can never mutate another's billing.
func RegisterCustomerSubscriptionRoutes(rg *gin.RouterGroup) {
	rg.GET("/subscriptions", handleListCustomerSubscriptions)
	rg.GET("/subscriptions/plans", handleListChangePlanOptions)
	rg.POST("/subscriptions/:publicId/cancel", handleCancelCustomerSubscription)
	rg.POST("/subscriptions/:publicId/resume", handleResumeCustomerSubscription)
	rg.POST("/subscriptions/:publicId/pause", handlePauseCustomerSubscription)
	rg.POST("/subscriptions/:publicId/resume-paused", handleResumePausedCustomerSubscription)
	rg.POST("/subscriptions/:publicId/change-plan", handleChangeCustomerSubscriptionPlan)
}

// customerSubscriptionDTO is the portal-facing shape: local billing state
// joined with a plan summary from the Offer.
type customerSubscriptionDTO struct {
	PublicID          string    `json:"public_id"`
	Status            string    `json:"status"`
	PlanTitle         string    `json:"plan_title"`
	AmountMinor       int64     `json:"amount_minor"`
	Currency          string    `json:"currency"`
	OfferPublicID     string    `json:"offer_public_id"`
	CurrentPeriodEnd  time.Time `json:"current_period_end,omitempty"`
	CancelAtPeriodEnd bool      `json:"cancel_at_period_end"`
	Paused            bool      `json:"paused"`
}

func toCustomerSubscriptionDTO(a pkgmodels.RecurringAgreement, offer *pkgmodels.Offer) customerSubscriptionDTO {
	dto := customerSubscriptionDTO{
		PublicID:          a.PublicId,
		Status:            a.Status,
		CurrentPeriodEnd:  a.CurrentPeriodEnd,
		CancelAtPeriodEnd: a.CancelAtPeriodEnd,
		Paused:            a.Paused,
	}
	if offer != nil {
		dto.PlanTitle = offer.Title
		dto.AmountMinor = offer.Amount
		dto.Currency = offer.Currency
		dto.OfferPublicID = offer.PublicId
	}
	return dto
}

func handleListCustomerSubscriptions(c *gin.Context) {
	tenantID, contactID, ok := resolveCustomerContext(c)
	if !ok {
		return
	}
	var agreements []pkgmodels.RecurringAgreement
	if err := db.GetCollection(pkgmodels.RecurringAgreementCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"contact_id": contactID,
	}).All(&agreements); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load subscriptions"})
		return
	}
	out := make([]customerSubscriptionDTO, 0, len(agreements))
	for _, a := range agreements {
		var offer pkgmodels.Offer
		var offerPtr *pkgmodels.Offer
		if err := db.GetCollection(pkgmodels.OfferCollection).FindId(a.OfferID).One(&offer); err == nil {
			offerPtr = &offer
		}
		out = append(out, toCustomerSubscriptionDTO(a, offerPtr))
	}
	c.JSON(http.StatusOK, gin.H{"subscriptions": out})
}

// handleListChangePlanOptions returns the tenant's published, Stripe-priced
// offers — the set a customer can switch an existing subscription to. Offers
// without a reusable Stripe price (ad-hoc price_data) are excluded because they
// can't back a subscription item swap.
func handleListChangePlanOptions(c *gin.Context) {
	tenantID, _, ok := resolveCustomerContext(c)
	if !ok {
		return
	}
	var offers []pkgmodels.Offer
	_ = db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
		"tenant_id":        tenantID,
		"stripe_price_id":  bson.M{"$nin": []interface{}{"", nil}},
		"status":           bson.M{"$ne": "archived"},
	}).All(&offers)
	out := make([]gin.H, 0, len(offers))
	for _, o := range offers {
		out = append(out, gin.H{
			"offer_public_id": o.PublicId,
			"title":           o.Title,
			"amount_minor":    o.Amount,
			"currency":        o.Currency,
		})
	}
	c.JSON(http.StatusOK, gin.H{"plans": out})
}

// loadOwnedAgreement fetches the agreement by public id, scoped to the
// authenticated contact. Writes the HTTP error and returns ok=false on miss.
func loadOwnedAgreement(c *gin.Context, tenantID, contactID bson.ObjectId) (pkgmodels.RecurringAgreement, bool) {
	var a pkgmodels.RecurringAgreement
	if err := db.GetCollection(pkgmodels.RecurringAgreementCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"contact_id": contactID,
		"public_id":  c.Param("publicId"),
	}).One(&a); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "subscription not found"})
		return pkgmodels.RecurringAgreement{}, false
	}
	if a.StripeSubscriptionID == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "subscription is not backed by Stripe billing"})
		return pkgmodels.RecurringAgreement{}, false
	}
	return a, true
}

// tenantStripeCreds resolves the tenant's Stripe credentials (own key, or the
// platform key + Connect account). Writes the HTTP error and returns ok=false
// when Stripe isn't usable. Mirrors migration_subscriptions.go.
func tenantStripeCreds(c *gin.Context, tenantID bson.ObjectId) (stripeKey, stripeAccount string, ok bool) {
	var tenant pkgmodels.Tenant
	if err := db.GetCollection(pkgmodels.TenantCollection).FindId(tenantID).One(&tenant); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "tenant load failed"})
		return "", "", false
	}
	stripeKey = utils.DecryptSecret(tenant.StripeSecretKey)
	if stripeKey == "" && tenant.StripeConnectAccountID != "" {
		stripeKey = os.Getenv("STRIPE_PLATFORM_SECRET_KEY")
		stripeAccount = tenant.StripeConnectAccountID
	}
	if stripeKey == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "billing is not configured for this business"})
		return "", "", false
	}
	if strings.HasPrefix(stripeKey, "sk_live") && os.Getenv("SENTANYL_ENV") != "production" {
		c.JSON(http.StatusForbidden, gin.H{"error": "live Stripe key outside production — refusing to mutate live billing"})
		return "", "", false
	}
	return stripeKey, stripeAccount, true
}

// applyStripeState persists the live Stripe billing state onto the agreement
// so the portal reflects the change immediately (the webhook will also
// reconcile, idempotently).
func applyStripeState(a *pkgmodels.RecurringAgreement, info checkout.StripeSubscriptionInfo) {
	set := bson.M{
		"cancel_at_period_end":  info.CancelAtPeriodEnd,
		"paused":                info.Paused,
		"timestamps.updated_at": time.Now(),
	}
	if info.Status != "" {
		set["status"] = info.Status
		a.Status = info.Status
	}
	if !info.CurrentPeriodEnd.IsZero() {
		set["current_period_end"] = info.CurrentPeriodEnd
		a.CurrentPeriodEnd = info.CurrentPeriodEnd
	}
	a.CancelAtPeriodEnd = info.CancelAtPeriodEnd
	a.Paused = info.Paused
	_ = db.GetCollection(pkgmodels.RecurringAgreementCollection).UpdateId(a.Id, bson.M{"$set": set})
}

// stripeMutation runs a common cancel/resume/pause action: load the owned
// agreement, resolve creds, call the Stripe op, persist, and return the DTO.
func stripeMutation(c *gin.Context, op func(key, account, subID string) (checkout.StripeSubscriptionInfo, error)) {
	tenantID, contactID, ok := resolveCustomerContext(c)
	if !ok {
		return
	}
	a, ok := loadOwnedAgreement(c, tenantID, contactID)
	if !ok {
		return
	}
	key, account, ok := tenantStripeCreds(c, tenantID)
	if !ok {
		return
	}
	info, err := op(key, account, a.StripeSubscriptionID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "billing update failed: " + err.Error()})
		return
	}
	applyStripeState(&a, info)
	var offer pkgmodels.Offer
	var offerPtr *pkgmodels.Offer
	if err := db.GetCollection(pkgmodels.OfferCollection).FindId(a.OfferID).One(&offer); err == nil {
		offerPtr = &offer
	}
	c.JSON(http.StatusOK, gin.H{"subscription": toCustomerSubscriptionDTO(a, offerPtr)})
}

func handleCancelCustomerSubscription(c *gin.Context) {
	stripeMutation(c, func(key, account, subID string) (checkout.StripeSubscriptionInfo, error) {
		return checkout.SetSubscriptionCancelAtPeriodEnd(key, account, subID, true)
	})
}

func handleResumeCustomerSubscription(c *gin.Context) {
	stripeMutation(c, func(key, account, subID string) (checkout.StripeSubscriptionInfo, error) {
		return checkout.SetSubscriptionCancelAtPeriodEnd(key, account, subID, false)
	})
}

func handlePauseCustomerSubscription(c *gin.Context) {
	stripeMutation(c, func(key, account, subID string) (checkout.StripeSubscriptionInfo, error) {
		return checkout.SetSubscriptionPause(key, account, subID, true)
	})
}

func handleResumePausedCustomerSubscription(c *gin.Context) {
	stripeMutation(c, func(key, account, subID string) (checkout.StripeSubscriptionInfo, error) {
		return checkout.SetSubscriptionPause(key, account, subID, false)
	})
}

// resolveChangePlanPrice validates a change-plan target: the offer must be a
// published, Stripe-priced offer belonging to the tenant. Pure so it can be
// unit-tested without mongo/Stripe.
func resolveChangePlanPrice(offer *pkgmodels.Offer) (priceID string, err error) {
	if offer == nil {
		return "", errChangePlan("target offer not found")
	}
	if offer.Status == "archived" {
		return "", errChangePlan("target plan is archived")
	}
	if offer.StripePriceID == "" {
		return "", errChangePlan("target plan has no reusable Stripe price — it can't be switched to")
	}
	return offer.StripePriceID, nil
}

type changePlanError struct{ msg string }

func (e changePlanError) Error() string { return e.msg }
func errChangePlan(m string) error      { return changePlanError{msg: m} }

func handleChangeCustomerSubscriptionPlan(c *gin.Context) {
	tenantID, contactID, ok := resolveCustomerContext(c)
	if !ok {
		return
	}
	var body struct {
		OfferPublicID string `json:"offer_public_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.OfferPublicID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "offer_public_id is required"})
		return
	}
	a, ok := loadOwnedAgreement(c, tenantID, contactID)
	if !ok {
		return
	}
	var target pkgmodels.Offer
	if err := db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
		"tenant_id": tenantID,
		"public_id": body.OfferPublicID,
	}).One(&target); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "target plan not found"})
		return
	}
	priceID, err := resolveChangePlanPrice(&target)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	key, account, ok := tenantStripeCreds(c, tenantID)
	if !ok {
		return
	}
	// Read the live subscription to get the current item id to swap.
	live, err := checkout.GetSubscription(key, account, a.StripeSubscriptionID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not read current plan: " + err.Error()})
		return
	}
	if live.ItemID == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "subscription has no swappable item"})
		return
	}
	info, err := checkout.ChangeSubscriptionPrice(key, account, a.StripeSubscriptionID, live.ItemID, priceID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "plan change failed: " + err.Error()})
		return
	}
	// Point the agreement at the new offer, then persist the fresh billing state.
	a.OfferID = target.Id
	_ = db.GetCollection(pkgmodels.RecurringAgreementCollection).UpdateId(a.Id, bson.M{"$set": bson.M{"offer_id": target.Id}})
	applyStripeState(&a, info)
	c.JSON(http.StatusOK, gin.H{"subscription": toCustomerSubscriptionDTO(a, &target)})
}
