package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/email"
	"github.com/josephalai/sentanyl/marketing-service/internal/analytics"
	"github.com/josephalai/sentanyl/marketing-service/internal/webhooks"
	"github.com/josephalai/sentanyl/marketing-service/routes"
	"github.com/josephalai/sentanyl/pkg/auth"
	badgecmd "github.com/josephalai/sentanyl/pkg/badges"
	"github.com/josephalai/sentanyl/pkg/db"
	httputil "github.com/josephalai/sentanyl/pkg/http"
	"github.com/josephalai/sentanyl/pkg/jobs"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

const commerceWebhookJobType = "commerce.webhook.process"

func RegisterCommerceWebhookJobs() {
	jobs.Register(commerceWebhookJobType, func(_ context.Context, job *jobs.Job) error {
		var tenant pkgmodels.Tenant
		if err := db.GetCollection(pkgmodels.TenantCollection).FindId(job.TenantID).One(&tenant); err != nil {
			return fmt.Errorf("load tenant: %w", err)
		}
		eventType, _ := job.Payload["event_type"].(string)
		object, _ := job.Payload["object"].(string)
		return processStripeEvent(job.TenantID, &tenant, eventType, json.RawMessage(object))
	})
}

// RegisterStripeWebhookRoute registers the public Stripe webhook receiver.
// Stripe calls this URL for every tenant using a platform-wide endpoint with
// ?tenant_id=<hex> to dispatch into the correct tenant's webhook secret.
func RegisterStripeWebhookRoute(publicAPI *gin.RouterGroup) {
	publicAPI.POST("/stripe/webhook", handleStripeWebhook)
}

// stripeEvent is the minimal shape we care about.
type stripeEvent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

// stripeCheckoutSession is the subset of Session fields we use.
type stripeCheckoutSession struct {
	ID              string `json:"id"`
	Mode            string `json:"mode"`
	AmountTotal     int64  `json:"amount_total"`
	Currency        string `json:"currency"`
	PaymentIntent   string `json:"payment_intent"`
	CustomerEmail   string `json:"customer_email"`
	CustomerDetails struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"customer_details"`
	Customer     string            `json:"customer"`
	Subscription string            `json:"subscription"`
	Metadata     map[string]string `json:"metadata"`
}

// stripeSubscription is the subset of Subscription fields we use.
type stripeSubscription struct {
	ID                string            `json:"id"`
	Status            string            `json:"status"`
	Metadata          map[string]string `json:"metadata"`
	CancelAtPeriodEnd bool              `json:"cancel_at_period_end"`
	CurrentPeriodEnd  int64             `json:"current_period_end"`
	PauseCollection   *struct {
		Behavior string `json:"behavior"`
	} `json:"pause_collection"`
}

// stripeInvoice is the subset of Invoice fields we use.
type stripeInvoice struct {
	Subscription string `json:"subscription"`
}

func handleStripeWebhook(c *gin.Context) {
	tenantIDHex := c.Query("tenant_id")
	if !bson.IsObjectIdHex(tenantIDHex) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id is required"})
		return
	}
	tenantID := bson.ObjectIdHex(tenantIDHex)

	var tenant pkgmodels.Tenant
	if err := db.GetCollection(pkgmodels.TenantCollection).FindId(tenantID).One(&tenant); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tenant not found"})
		return
	}
	if tenant.StripeWebhookSecret == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "tenant webhook secret not configured"})
		return
	}

	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}

	if err := verifyStripeSignature(c.GetHeader("Stripe-Signature"), rawBody, utils.DecryptSecret(tenant.StripeWebhookSecret)); err != nil {
		log.Printf("[stripe webhook] signature verify failed: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid signature"})
		return
	}

	var evt stripeEvent
	if err := json.Unmarshal(rawBody, &evt); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}

	if evt.ID == "" {
		sum := sha256.Sum256(rawBody)
		evt.ID = fmt.Sprintf("body-%x", sum[:])
	}
	key := tenantID.Hex() + ":" + evt.ID
	job := jobs.NewJob(commerceWebhookJobType, key, jobs.Envelope{
		TenantID: tenantID, Actor: "stripe", Subject: evt.Type, Version: 1,
		CorrelationID: evt.ID, CausationID: evt.ID,
	}, bson.M{"event_type": evt.Type, "object": string(evt.Data.Object)})
	if err := jobs.Enqueue(job); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not persist webhook"})
		return
	}
	var durable jobs.Job
	if err := db.GetCollection(jobs.JobCollection).Find(bson.M{"type": commerceWebhookJobType, "idempotency_key": key}).One(&durable); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not reload webhook"})
		return
	}
	if durable.Status == jobs.StatusSucceeded {
		c.JSON(http.StatusOK, gin.H{"received": true, "duplicate": true})
		return
	}
	if os.Getenv("SENTANYL_E2E_MODE") == "1" && c.GetHeader("X-E2E-Fail-Point") == "after-commerce-enqueue" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "injected failure after durable enqueue"})
		return
	}
	// Claim an HTTP-side lease before doing synchronous work. Without this CAS,
	// the background worker can claim the freshly inserted pending row while the
	// request is still processing and duplicate non-ledger side effects (email,
	// outbound events). A crashed request is reclaimed after the lease expires;
	// explicit processing failure releases it immediately.
	now := time.Now()
	err = db.GetCollection(jobs.JobCollection).Update(bson.M{
		"_id": durable.Id,
		"$or": []bson.M{
			{"status": bson.M{"$in": []string{jobs.StatusPending, jobs.StatusFailed}}},
			{"status": jobs.StatusRunning, "lease_expires_at": bson.M{"$lte": now}},
		},
	}, bson.M{"$set": bson.M{
		"status": jobs.StatusRunning, "lease_owner": "stripe-http",
		"lease_expires_at": now.Add(2 * time.Minute), "updated_at": now,
	}})
	if err != nil {
		// Another delivery or worker owns the live lease. It will complete the
		// durable event; acknowledge without executing it a second time.
		c.JSON(http.StatusAccepted, gin.H{"received": true, "processing": true})
		return
	}
	if err := processStripeEvent(tenantID, &tenant, evt.Type, evt.Data.Object); err != nil {
		_ = db.GetCollection(jobs.JobCollection).UpdateId(durable.Id, bson.M{"$set": bson.M{
			"status": jobs.StatusPending, "run_at": time.Now(), "lease_owner": "",
			"lease_expires_at": time.Time{}, "last_error": err.Error(), "updated_at": time.Now(),
		}})
		log.Printf("[stripe webhook] %s: %v", evt.Type, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_ = jobs.Complete(durable.Id)
	c.JSON(http.StatusOK, gin.H{"received": true})
}

func processStripeEvent(tenantID bson.ObjectId, tenant *pkgmodels.Tenant, eventType string, object json.RawMessage) error {
	switch eventType {
	case "checkout.session.completed":
		return processCheckoutSessionCompleted(tenantID, tenant, object)
	case "invoice.paid":
		return processInvoicePaid(tenantID, object)
	case "customer.subscription.deleted", "customer.subscription.updated":
		return processSubscriptionStateChange(tenantID, object)
	case "charge.refunded", "charge.refund.updated":
		return processChargeRefunded(tenantID, object)
	case "charge.dispute.created", "charge.dispute.updated":
		return processChargeDisputed(tenantID, object)
	default:
		return nil
	}
}

// verifyStripeSignature checks a Stripe-Signature header against the request body.
// Delegates to the shared verifier also used by the platform billing webhook.
func verifyStripeSignature(header string, body []byte, secret string) error {
	return httputil.VerifyStripeSignature(header, body, secret)
}

func processCheckoutSessionCompleted(tenantID bson.ObjectId, tenant *pkgmodels.Tenant, raw json.RawMessage) error {
	var session stripeCheckoutSession
	if err := json.Unmarshal(raw, &session); err != nil {
		return fmt.Errorf("decode session: %w", err)
	}

	// MIG-007: subscription-takeover journeys carry no offer metadata — they
	// settle the MigratedSubscription record instead of provisioning.
	if msubID := session.Metadata["migrated_subscription_id"]; msubID != "" {
		if routes.SettleMigratedSubscription(tenantID, msubID, session.Subscription) {
			log.Printf("[stripe webhook] migrated subscription %s activated (stripe sub %s)", msubID, session.Subscription)
		}
		return nil
	}

	offerIDHex := session.Metadata["offer_id"]
	if !bson.IsObjectIdHex(offerIDHex) {
		return fmt.Errorf("metadata.offer_id missing or invalid")
	}
	offerID := bson.ObjectIdHex(offerIDHex)
	domain := session.Metadata["domain"]

	email := strings.ToLower(strings.TrimSpace(session.CustomerEmail))
	if email == "" {
		email = strings.ToLower(strings.TrimSpace(session.CustomerDetails.Email))
	}
	if email == "" {
		return fmt.Errorf("no customer email in session")
	}

	var offer pkgmodels.Offer
	if err := db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
		"_id":       offerID,
		"tenant_id": tenantID,
	}).One(&offer); err != nil {
		return fmt.Errorf("offer not found: %w", err)
	}

	contact, isNewBuyer, err := upsertContactForCheckout(tenantID, email, session.CustomerDetails.Name, session.Customer)
	if err != nil {
		return fmt.Errorf("upsert contact: %w", err)
	}

	if err := grantOfferBadges(tenantID, contact.Id, offer.GrantedBadges, session.ID); err != nil {
		return fmt.Errorf("grant badges: %w", err)
	}

	// Revenue trail: write one PurchaseLog row for the purchase. We use the
	// session's amount_total (post-discount) so revenue queries reflect what
	// the buyer actually paid. PaymentIntent (or the session id as fallback)
	// is recorded as StripeChargeId so refund processing can mark this row
	// refunded later.
	if err := recordPurchaseLog(tenantID, contact, &offer, &session); err != nil {
		log.Printf("[stripe webhook] purchase log: %v", err)
	}

	// Newsletter side-effect: if this offer is bound to any newsletter tier,
	// upsert/upgrade the contact's NewsletterSubscription to that tier and
	// flip status to active. The paywall renderer reads tier_id off these
	// rows when gating post bodies.
	if err := upgradeNewsletterSubscriptionForOffer(tenantID, contact.Id, email, offer.Id, session.Subscription); err != nil {
		log.Printf("[stripe webhook] newsletter tier upgrade: %v", err)
	}

	// Record the immutable Purchase + PurchaseItem ledger (COM-CC-005/006).
	// The Purchase is idempotent by Stripe session id, so retries reuse it; a
	// fresh checkout (repurchase) is a new session and therefore a new Purchase
	// with new items — never deduped by tenant+contact+product.
	purchase, err := recordPurchaseLedger(tenantID, contact.Id, &offer, &session)
	if err != nil {
		return fmt.Errorf("record purchase ledger: %w", err)
	}
	if err := recordRecurringAgreement(tenantID, contact.Id, offer.Id, purchase.Id, session.Subscription); err != nil {
		return fmt.Errorf("record recurring agreement: %w", err)
	}

	// Emit a durable, signed outbound webhook for the purchase (WH-003/004).
	// Best-effort enqueue: delivery + retry are handled by the job worker.
	if err := webhooks.Emit(tenantID, "purchase.completed", map[string]interface{}{
		"purchase_public_id": purchase.PublicId,
		"offer_public_id":    offer.PublicId,
		"contact_public_id":  contact.PublicId,
		"amount_total":       purchase.AmountTotal,
		"currency":           purchase.Currency,
	}); err != nil {
		log.Printf("[stripe webhook] webhook emit failed: %v", err)
	}

	// Provision each product included in the offer, keyed off its PurchaseItem
	// so retries never double-provision and a partial failure is recoverable:
	// each item is provisioned only while its status is not yet "provisioned".
	// Dispatch by product type wires the right downstream system (courses → LMS
	// enrollment, services → ServiceEnrollment, coaching → coaching-service,
	// downloads/newsletters → badge/tier above).
	var enrollFailures []string
	for _, snap := range purchase.OfferSnapshot.Items {
		productID := snap.ProductID
		item, err := ensurePurchaseItem(tenantID, contact.Id, purchase, snap)
		if err != nil {
			enrollFailures = append(enrollFailures, fmt.Sprintf("%s: item: %v", productID.Hex(), err))
			continue
		}
		if item.Status == pkgmodels.ItemStatusProvisioned {
			continue // already provisioned on an earlier delivery of this event
		}
		if item.FulfillmentPolicy.Mode == pkgmodels.OfferFulfillmentManual {
			continue // an operator must fulfill this line; pending is intentional
		}
		if err := provisionProductPurchase(tenantID, contact.Id, productID, offer.Id, item.Id); err != nil {
			log.Printf("[stripe webhook] PROVISION FAILED tenant=%s offer=%s product=%s contact=%s email=%s: %v",
				tenantID.Hex(), offer.Id.Hex(), productID.Hex(), contact.Id.Hex(), email, err)
			enrollFailures = append(enrollFailures, fmt.Sprintf("%s: %v", productID.Hex(), err))
			continue
		}
		markPurchaseItemProvisioned(item.Id)
		// Durable, customer-specific entitlement derived from the purchase item
		// (COM-CC-001/007). Idempotent by purchase_item_id.
		ensureAccessGrant(item, offer.Id)
	}

	if isNewBuyer {
		token, expires, err := setPasswordResetToken(contact.Id)
		if err != nil {
			log.Printf("[stripe webhook] password token: %v", err)
		} else {
			portalURL := buildPortalSetPasswordURL(domain, token)
			log.Printf("[stripe webhook] password setup URL for %s: %s (expires %s)", email, portalURL, expires.Format(time.RFC3339))
			if err := sendPasswordSetupEmail(tenant, email, portalURL); err != nil {
				log.Printf("[stripe webhook] email send: %v", err)
			}
		}
	}

	if len(enrollFailures) > 0 {
		return fmt.Errorf("enrollment failed for %d of %d products in offer %s: %s",
			len(enrollFailures), len(purchase.OfferSnapshot.Items), offer.Id.Hex(), strings.Join(enrollFailures, "; "))
	}
	return nil
}

func upsertContactForCheckout(tenantID bson.ObjectId, email, name, stripeCustomerID string) (*pkgmodels.User, bool, error) {
	col := db.GetCollection(pkgmodels.UserCollection)
	var existing pkgmodels.User
	err := col.Find(bson.M{
		"email":     pkgmodels.EmailAddress(email),
		"tenant_id": tenantID,
	}).One(&existing)
	if err == nil {
		updates := bson.M{"timestamps.updated_at": time.Now()}
		if stripeCustomerID != "" && existing.StripeCustomerID != stripeCustomerID {
			updates["stripe_customer_id"] = stripeCustomerID
		}
		if name != "" && existing.Name.First == "" {
			parts := strings.SplitN(name, " ", 2)
			updates["name.first_name"] = parts[0]
			if len(parts) > 1 {
				updates["name.last_name"] = parts[1]
			}
		}
		_ = col.Update(bson.M{"_id": existing.Id}, bson.M{"$set": updates})
		return &existing, existing.PasswordHash == "", nil
	}

	now := time.Now()
	contact := pkgmodels.User{
		Id:               bson.NewObjectId(),
		PublicId:         utils.GeneratePublicId(),
		TenantID:         tenantID,
		Email:            pkgmodels.EmailAddress(email),
		StripeCustomerID: stripeCustomerID,
	}
	if name != "" {
		parts := strings.SplitN(name, " ", 2)
		contact.Name.First = parts[0]
		if len(parts) > 1 {
			contact.Name.Last = parts[1]
		}
	}
	contact.SoftDeletes.CreatedAt = &now
	if err := col.Insert(contact); err != nil {
		return nil, false, err
	}
	return &contact, true, nil
}

// grantOfferBadges resolves or creates tenant-scoped Badge docs for each granted
// badge name and grants them through the provenance command (ID-012),
// idempotent per (checkout session, badge).
func grantOfferBadges(tenantID, contactID bson.ObjectId, badgeNames []string, sourceRef string) error {
	if len(badgeNames) == 0 {
		return nil
	}
	badgeCol := db.GetCollection(pkgmodels.BadgeCollection)
	var badgeIDs []bson.ObjectId
	for _, name := range badgeNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var existing pkgmodels.Badge
		err := badgeCol.Find(bson.M{"tenant_id": tenantID, "name": name}).One(&existing)
		if err == nil {
			badgeIDs = append(badgeIDs, existing.Id)
			continue
		}
		now := time.Now()
		badge := pkgmodels.Badge{
			Id:       bson.NewObjectId(),
			PublicId: utils.GeneratePublicId(),
			TenantID: tenantID,
			Name:     name,
		}
		badge.SoftDeletes.CreatedAt = &now
		if err := badgeCol.Insert(badge); err != nil {
			return fmt.Errorf("create badge %q: %w", name, err)
		}
		badgeIDs = append(badgeIDs, badge.Id)
	}
	for _, id := range badgeIDs {
		if _, err := badgecmd.Assign(tenantID, contactID, id, "offer_purchase", sourceRef, "system"); err != nil {
			return err
		}
	}
	return nil
}

// recordPurchaseLog inserts one PurchaseLog row per included product on a
// successful checkout. Idempotent on (tenant, stripe_charge_id) — replays of
// the same Stripe event don't double-count revenue. The session's
// amount_total is split evenly across included products so revenue rollups
// in serve_revenue.go (by-product, by-contact) can attribute each line item
// to the right entitlement. Offers with no IncludedProducts get a single
// offer-level row (ProductId zero).
func recordPurchaseLog(tenantID bson.ObjectId, contact *pkgmodels.User, offer *pkgmodels.Offer, session *stripeCheckoutSession) error {
	chargeRef := session.PaymentIntent
	if chargeRef == "" {
		chargeRef = session.ID
	}
	col := db.GetCollection(pkgmodels.PurchaseLogCollection)
	if chargeRef != "" {
		// Even one row matching this charge means we already recorded the
		// purchase — no-op for replays.
		var existing pkgmodels.PurchaseLog
		if err := col.Find(bson.M{
			"tenant_id":        tenantID,
			"stripe_charge_id": chargeRef,
		}).One(&existing); err == nil {
			return nil
		}
	}

	totalAmount := float64(session.AmountTotal) / 100
	currency := strings.ToLower(strings.TrimSpace(session.Currency))
	if currency == "" {
		currency = strings.ToLower(strings.TrimSpace(offer.Currency))
	}
	if totalAmount == 0 && offer.Amount > 0 {
		totalAmount = float64(offer.Amount) / 100
	}

	productIDs := offer.IncludedProducts
	if len(productIDs) == 0 {
		// No bundle — write a single offer-level row.
		productIDs = []bson.ObjectId{""}
	}
	share := totalAmount
	if len(productIDs) > 1 {
		share = totalAmount / float64(len(productIDs))
	}

	// Phase 11A Step 3: stamp the originating video session id when the
	// buyer was watching a Sentanyl video on the page that launched
	// checkout. The runtime player propagates session_public_id through
	// /api/marketing/site/checkout/start → Stripe metadata → this handler.
	videoSessionID := session.Metadata["video_session_id"]

	now := time.Now()
	for _, pid := range productIDs {
		entry := pkgmodels.PurchaseLog{
			Id:                   bson.NewObjectId(),
			PublicId:             utils.GeneratePublicId(),
			TenantID:             tenantID,
			SubscriberId:         tenantID.Hex(),
			UserId:               contact.Id,
			ProductId:            pid,
			OfferID:              offer.Id,
			Amount:               share,
			Currency:             currency,
			StripeChargeId:       chargeRef,
			Status:               "paid",
			VideoSessionPublicId: videoSessionID,
		}
		entry.SoftDeletes.CreatedAt = &now
		if err := col.Insert(entry); err != nil {
			return err
		}
		// ANA-005/006: project the immutable sale fact (idempotent), with
		// last-touch attribution resolved inside the window.
		analytics.WriteSaleFact(&entry)
	}
	return nil
}

// recordPurchaseLedger idempotently creates the immutable Purchase for a
// checkout session (COM-CC-005). Keyed by (tenant, stripe_session_id): a Stripe
// retry of the same event reuses the existing Purchase, while a new checkout is
// a distinct session and therefore a new Purchase.
func recordPurchaseLedger(tenantID, contactID bson.ObjectId, offer *pkgmodels.Offer, session *stripeCheckoutSession) (*pkgmodels.Purchase, error) {
	col := db.GetCollection(pkgmodels.PurchaseCollection)
	var existing pkgmodels.Purchase
	if err := col.Find(bson.M{"tenant_id": tenantID, "stripe_session_id": session.ID}).One(&existing); err == nil {
		return &existing, nil
	}
	items := offer.Items
	if len(items) == 0 {
		items = make([]pkgmodels.OfferItem, 0, len(offer.IncludedProducts))
		for _, productID := range offer.IncludedProducts {
			items = append(items, pkgmodels.DefaultOfferItem(productID))
		}
	}
	itemSnaps := make([]pkgmodels.OfferItemSnapshot, 0, len(items))
	productIDs := make([]bson.ObjectId, 0, len(items))
	for _, item := range items {
		var product pkgmodels.Product
		if err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{"_id": item.ProductID, "tenant_id": tenantID}).One(&product); err != nil {
			return nil, fmt.Errorf("snapshot product %s: %w", item.ProductID.Hex(), err)
		}
		itemSnaps = append(itemSnaps, pkgmodels.OfferItemSnapshot{
			ProductID: item.ProductID, ProductType: product.ProductType, ProductTitle: product.Name,
			PolicyVersion: item.Version, AccessPolicy: item.AccessPolicy, FulfillmentPolicy: item.FulfillmentPolicy,
		})
		productIDs = append(productIDs, item.ProductID)
	}
	snap := pkgmodels.OfferSnapshot{
		OfferID:       offer.Id,
		Title:         offer.Title,
		PricingModel:  offer.PricingModel,
		Amount:        offer.Amount,
		Currency:      offer.Currency,
		GrantedBadges: offer.GrantedBadges,
		ProductIDs:    productIDs,
		Items:         itemSnaps,
	}
	currency := session.Currency
	if currency == "" {
		currency = offer.Currency
	}
	p := pkgmodels.NewPurchase(tenantID, contactID, snap, session.AmountTotal, currency, session.ID)
	p.StripePaymentIntentID = session.PaymentIntent
	p.StripeSubscriptionID = session.Subscription
	if err := col.Insert(p); err != nil {
		// A concurrent delivery may have won the race; re-read.
		if err2 := col.Find(bson.M{"tenant_id": tenantID, "stripe_session_id": session.ID}).One(&existing); err2 == nil {
			return &existing, nil
		}
		return nil, err
	}
	return p, nil
}

// ensurePurchaseItem idempotently creates the PurchaseItem for one product of a
// Purchase (COM-CC-006), keyed by (purchase_id, product_id). Returns the item
// (existing or newly created) so the caller can decide whether to provision.
func ensurePurchaseItem(tenantID, contactID bson.ObjectId, purchase *pkgmodels.Purchase, snap pkgmodels.OfferItemSnapshot) (*pkgmodels.PurchaseItem, error) {
	col := db.GetCollection(pkgmodels.PurchaseItemCollection)
	var existing pkgmodels.PurchaseItem
	if err := col.Find(bson.M{"purchase_id": purchase.Id, "product_id": snap.ProductID}).One(&existing); err == nil {
		return &existing, nil
	}
	item := pkgmodels.NewPurchaseItemFromSnapshot(tenantID, contactID, purchase.Id, purchase.OfferSnapshot.OfferID, snap)
	if err := col.Insert(item); err != nil {
		if err2 := col.Find(bson.M{"purchase_id": purchase.Id, "product_id": snap.ProductID}).One(&existing); err2 == nil {
			return &existing, nil
		}
		return nil, err
	}
	return item, nil
}

// markPurchaseItemProvisioned flips a purchase item to provisioned after its
// downstream provisioning succeeds.
func markPurchaseItemProvisioned(itemID bson.ObjectId) {
	_ = db.GetCollection(pkgmodels.PurchaseItemCollection).UpdateId(itemID, bson.M{
		"$set": bson.M{"status": pkgmodels.ItemStatusProvisioned, "timestamps.updated_at": time.Now()},
	})
}

// ensureAccessGrant creates the durable AccessGrant for a provisioned purchase
// item, idempotent by purchase_item_id (COM-CC-001/007). This is the
// authoritative entitlement the customer Library authorizes against.
func ensureAccessGrant(item *pkgmodels.PurchaseItem, offerID bson.ObjectId) {
	col := db.GetCollection(pkgmodels.AccessGrantCollection)
	n, _ := col.Find(bson.M{"purchase_item_id": item.Id}).Count()
	if n > 0 {
		return
	}
	grant := pkgmodels.NewAccessGrant(item.TenantID, item.ContactID, item.ProductID, item.Id, offerID, "purchase")
	if item.AccessPolicy.Mode == pkgmodels.OfferAccessFixedDays && item.AccessPolicy.DurationDays > 0 {
		expires := time.Now().Add(time.Duration(item.AccessPolicy.DurationDays) * 24 * time.Hour)
		grant.ExpiresAt = &expires
	}
	if err := col.Insert(grant); err != nil {
		log.Printf("[stripe webhook] access grant insert failed for item %s: %v", item.Id.Hex(), err)
	}
}

// recordRecurringAgreement records billing state only when Stripe created a
// real subscription. One-time orders remain represented solely by Purchase.
func recordRecurringAgreement(tenantID, contactID, offerID, purchaseID bson.ObjectId, stripeSubscriptionID string) error {
	if stripeSubscriptionID == "" {
		return nil
	}
	col := db.GetCollection(pkgmodels.RecurringAgreementCollection)
	var existing pkgmodels.RecurringAgreement
	if err := col.Find(bson.M{"tenant_id": tenantID, "stripe_subscription_id": stripeSubscriptionID}).One(&existing); err == nil {
		return col.UpdateId(existing.Id, bson.M{"$set": bson.M{"status": "active", "timestamps.updated_at": time.Now()}})
	}
	return col.Insert(pkgmodels.NewRecurringAgreement(tenantID, contactID, offerID, purchaseID, stripeSubscriptionID))
}

// callInternalEnroll posts to lms-service /internal/enroll. The purchase
// item id is the idempotency key (DEL-007): retries reuse the enrollment,
// repurchases (new item) enroll again; offer/source record provenance
// (DEL-008).
func callInternalEnroll(tenantID, contactID, productID, offerID, purchaseItemID bson.ObjectId) error {
	lmsURL := os.Getenv("LMS_SERVICE_URL")
	if lmsURL == "" {
		lmsURL = "http://lms-service:8083"
	}
	payload := map[string]string{
		"tenant_id":  tenantID.Hex(),
		"contact_id": contactID.Hex(),
		"product_id": productID.Hex(),
		"source":     "purchase",
	}
	if offerID.Valid() {
		payload["offer_id"] = offerID.Hex()
	}
	if purchaseItemID.Valid() {
		payload["purchase_item_id"] = purchaseItemID.Hex()
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", lmsURL+"/internal/enroll", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	auth.AttachServiceAuth(req, "marketing") // API-001 signed service identity
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("lms-service returned %d: %s", resp.StatusCode, string(msg))
	}
	return nil
}

// setPasswordResetToken mints a setup token for the contact. Only the hash
// is persisted (ID-015); the returned plaintext is the single handoff and
// must reach the customer directly (email link / checkout success page).
func setPasswordResetToken(contactID bson.ObjectId) (string, time.Time, error) {
	token, hashed, err := auth.MintResetToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().Add(48 * time.Hour)
	err = db.GetCollection(pkgmodels.UserCollection).Update(
		bson.M{"_id": contactID},
		bson.M{"$set": bson.M{
			"password_reset_token":   hashed,
			"password_reset_expires": expires,
		}},
	)
	return token, expires, err
}

func buildPortalSetPasswordURL(domain, token string) string {
	if domain == "" {
		return "/portal/set-password?token=" + token
	}
	scheme := "https"
	if strings.Contains(domain, "lvh.me") || strings.Contains(domain, "localhost") {
		scheme = "http"
	}
	return scheme + "://" + domain + "/portal/set-password?token=" + token
}

func sendPasswordSetupEmail(tenant *pkgmodels.Tenant, toEmail, portalURL string) error {
	provider := selectMailProvider(tenant)
	if provider == nil {
		return nil
	}
	from := "no-reply@" + tenant.MailgunDomain
	if tenant.MailgunDomain == "" {
		from = "no-reply@sentanyl.local"
	}
	subject := "Set up your account"
	body := fmt.Sprintf(`<p>Thanks for your purchase from %s.</p>
<p>Click the link below to set your password and access your library:</p>
<p><a href="%s">%s</a></p>
<p>This link expires in 48 hours.</p>`, htmlEscape(tenant.BusinessName), portalURL, portalURL)
	return provider.SendEmail(from, toEmail, subject, body, "")
}

func selectMailProvider(tenant *pkgmodels.Tenant) email.EmailProvider {
	return email.DefaultProvider()
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

func processInvoicePaid(tenantID bson.ObjectId, raw json.RawMessage) error {
	var inv stripeInvoice
	if err := json.Unmarshal(raw, &inv); err != nil {
		return err
	}
	if inv.Subscription == "" {
		return nil
	}
	return db.GetCollection(pkgmodels.RecurringAgreementCollection).Update(
		bson.M{"tenant_id": tenantID, "stripe_subscription_id": inv.Subscription},
		bson.M{"$set": bson.M{"status": "active", "timestamps.updated_at": time.Now()}},
	)
}

// subscriptionAccessState maps a Stripe subscription status onto its access
// consequence (BILL-007): a lossless enum instead of copying the raw string.
//
//	active     — access on (restore a prior suspension)
//	suspend    — access paused (past_due/unpaid/paused/incomplete)
//	revoke     — access ends (canceled/incomplete_expired)
//	noop       — no access change (unknown/blank → leave grants as-is)
func subscriptionAccessState(status string) string {
	switch status {
	case "active", "trialing":
		return "active"
	case "past_due", "unpaid", "paused", "incomplete":
		return "suspend"
	case "canceled", "incomplete_expired":
		return "revoke"
	default:
		return "noop"
	}
}

func processSubscriptionStateChange(tenantID bson.ObjectId, raw json.RawMessage) error {
	var sub stripeSubscription
	if err := json.Unmarshal(raw, &sub); err != nil {
		return err
	}
	if sub.ID == "" {
		return nil
	}
	newStatus := sub.Status
	if newStatus == "" {
		newStatus = "canceled"
	}
	// Mirror the billing-schedule flags so the customer portal can render
	// "cancels on …" / "paused" without a live Stripe round-trip.
	set := bson.M{
		"status":                newStatus,
		"cancel_at_period_end":  sub.CancelAtPeriodEnd,
		"paused":                sub.PauseCollection != nil,
		"timestamps.updated_at": time.Now(),
	}
	if sub.CurrentPeriodEnd > 0 {
		set["current_period_end"] = time.Unix(sub.CurrentPeriodEnd, 0).UTC()
	}
	if err := db.GetCollection(pkgmodels.RecurringAgreementCollection).Update(
		bson.M{"tenant_id": tenantID, "stripe_subscription_id": sub.ID},
		bson.M{"$set": set},
	); err != nil {
		return err
	}

	// COM-CC-014/BILL-007: propagate the billing state onto the AccessGrant
	// authority so a lapsed/canceled subscription actually gates delivery.
	var subs []pkgmodels.RecurringAgreement
	_ = db.GetCollection(pkgmodels.RecurringAgreementCollection).Find(
		bson.M{"tenant_id": tenantID, "stripe_subscription_id": sub.ID}).All(&subs)
	action := subscriptionAccessState(newStatus)
	now := time.Now()
	for _, srow := range subs {
		grantFilter := bson.M{"tenant_id": tenantID, "contact_id": srow.ContactID, "offer_id": srow.OfferID}
		switch action {
		case "suspend":
			grantFilter["status"] = pkgmodels.GrantStatusActive
			_, _ = db.GetCollection(pkgmodels.AccessGrantCollection).UpdateAll(grantFilter,
				bson.M{"$set": bson.M{"status": pkgmodels.GrantStatusSuspended, "timestamps.updated_at": now}})
		case "active":
			// Restore only rows suspended by a prior billing lapse — never
			// un-revoke a refund/dispute revocation.
			grantFilter["status"] = pkgmodels.GrantStatusSuspended
			_, _ = db.GetCollection(pkgmodels.AccessGrantCollection).UpdateAll(grantFilter,
				bson.M{"$set": bson.M{"status": pkgmodels.GrantStatusActive, "timestamps.updated_at": now}})
		case "revoke":
			grantFilter["status"] = bson.M{"$in": []string{pkgmodels.GrantStatusActive, pkgmodels.GrantStatusSuspended}}
			_, _ = db.GetCollection(pkgmodels.AccessGrantCollection).UpdateAll(grantFilter,
				bson.M{"$set": bson.M{"status": pkgmodels.GrantStatusRevoked, "timestamps.updated_at": now}})
		}
	}
	return nil
}

// processChargeDisputed handles charge.dispute.created (COM-CC-014): a
// chargeback suspends the buyer's access immediately — the money is being
// clawed back — and marks the Purchase disputed for audit. Reuses the same
// offer-resolution chain as refunds. Suspension (not revocation) leaves room
// for a won dispute to restore access via a subsequent subscription.updated.
func processChargeDisputed(tenantID bson.ObjectId, raw json.RawMessage) error {
	var charge stripeCharge
	if err := json.Unmarshal(raw, &charge); err != nil {
		return err
	}
	offerHex := charge.Metadata["offer_id"]
	if offerHex == "" {
		offerHex = charge.PaymentIntentMeta["offer_id"]
	}
	if !bson.IsObjectIdHex(offerHex) {
		log.Printf("[stripe webhook] dispute: no resolvable offer_id on charge %s — skipping", charge.ID)
		return nil
	}
	offerID := bson.ObjectIdHex(offerHex)
	var offer pkgmodels.Offer
	if err := db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{"_id": offerID, "tenant_id": tenantID}).One(&offer); err != nil {
		return nil
	}
	contactEmail := strings.ToLower(strings.TrimSpace(charge.Metadata["contact_email"]))
	var contact pkgmodels.User
	if contactEmail != "" {
		_ = db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
			"tenant_id": tenantID, "email": pkgmodels.EmailAddress(contactEmail),
		}).One(&contact)
	}
	now := time.Now()
	if contact.Id.Valid() && len(offer.IncludedProducts) > 0 {
		_, _ = db.GetCollection(pkgmodels.AccessGrantCollection).UpdateAll(
			bson.M{"tenant_id": tenantID, "contact_id": contact.Id,
				"product_id": bson.M{"$in": offer.IncludedProducts},
				"status":     bson.M{"$in": []string{pkgmodels.GrantStatusActive, pkgmodels.GrantStatusSuspended}}},
			bson.M{"$set": bson.M{"status": pkgmodels.GrantStatusSuspended, "timestamps.updated_at": now}},
		)
	}
	// Mark the purchase disputed for audit (idempotent).
	_, _ = db.GetCollection(pkgmodels.PurchaseCollection).UpdateAll(
		bson.M{"tenant_id": tenantID, "offer_id": offerID, "stripe_charge_id": charge.ID},
		bson.M{"$set": bson.M{"disputed_at": now, "timestamps.updated_at": now}},
	)
	log.Printf("[stripe webhook] dispute on charge %s → suspended access for offer %s", charge.ID, offerID.Hex())
	return nil
}

// stripeCharge is the subset of Charge fields we use for refund handling.
type stripeCharge struct {
	ID                string            `json:"id"`
	Amount            int64             `json:"amount"`
	AmountRefunded    int64             `json:"amount_refunded"`
	Refunded          bool              `json:"refunded"`
	PaymentIntent     string            `json:"payment_intent"`
	Metadata          map[string]string `json:"metadata"`
	PaymentIntentMeta map[string]string `json:"payment_intent_metadata,omitempty"`
}

// processChargeRefunded handles the Stripe charge.refunded event by revoking
// any enrollments granted by the original purchase and stripping the offer's
// granted badges from the contact. Idempotent — already-revoked enrollments
// stay revoked.
//
// Lookup chain: charge.metadata.offer_id (set by checkout) OR the linked
// payment_intent's metadata; if neither resolves we log + skip rather than
// guess.
func processChargeRefunded(tenantID bson.ObjectId, raw json.RawMessage) error {
	var charge stripeCharge
	if err := json.Unmarshal(raw, &charge); err != nil {
		return err
	}

	offerHex := charge.Metadata["offer_id"]
	if offerHex == "" {
		offerHex = charge.PaymentIntentMeta["offer_id"]
	}
	contactEmail := strings.ToLower(strings.TrimSpace(charge.Metadata["contact_email"]))

	if offerHex == "" || !bson.IsObjectIdHex(offerHex) {
		log.Printf("[stripe webhook] refund: no resolvable offer_id on charge %s — skipping", charge.ID)
		return nil
	}
	offerID := bson.ObjectIdHex(offerHex)

	var offer pkgmodels.Offer
	if err := db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{"_id": offerID, "tenant_id": tenantID}).One(&offer); err != nil {
		return fmt.Errorf("offer %s lookup failed: %w", offerHex, err)
	}

	// Resolve one exact commercial acquisition. Stripe's payment intent is the
	// primary key. The contact+offer fallback exists only for local simulations
	// that cannot provide one, and is bounded to the latest unrefunded Purchase.
	var contact pkgmodels.User
	if contactEmail != "" {
		_ = db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
			"tenant_id": tenantID,
			"email":     pkgmodels.EmailAddress(contactEmail),
		}).One(&contact)
	}
	purchaseQuery := bson.M{"tenant_id": tenantID, "offer_snapshot.offer_id": offerID, "status": bson.M{"$ne": pkgmodels.PurchaseStatusRefunded}}
	if charge.PaymentIntent != "" {
		purchaseQuery["stripe_payment_intent_id"] = charge.PaymentIntent
	} else if contact.Id.Valid() {
		purchaseQuery["contact_id"] = contact.Id
	} else {
		return fmt.Errorf("could not resolve refund purchase for offer %s", offerHex)
	}
	var purchase pkgmodels.Purchase
	if err := db.GetCollection(pkgmodels.PurchaseCollection).Find(purchaseQuery).Sort("-timestamps.created_at").One(&purchase); err != nil {
		return fmt.Errorf("could not resolve refund purchase for offer %s: %w", offerHex, err)
	}
	if !contact.Id.Valid() {
		if err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{"_id": purchase.ContactID, "tenant_id": tenantID}).One(&contact); err != nil {
			return fmt.Errorf("contact %s missing", purchase.ContactID.Hex())
		}
	}

	now := time.Now()
	var items []pkgmodels.PurchaseItem
	if err := db.GetCollection(pkgmodels.PurchaseItemCollection).Find(bson.M{
		"tenant_id": tenantID, "purchase_id": purchase.Id,
	}).All(&items); err != nil {
		return fmt.Errorf("load refund purchase items: %w", err)
	}
	itemIDs := make([]bson.ObjectId, 0, len(items))
	for _, item := range items {
		itemIDs = append(itemIDs, item.Id)
	}

	// Revoke only fulfillment produced by this purchase's line items. A later
	// repurchase of the same product has distinct item ids and remains active.
	if len(itemIDs) > 0 {
		if _, err := db.GetCollection(pkgmodels.CourseEnrollmentCollection).UpdateAll(
			bson.M{
				"tenant_id": tenantID, "purchase_item_id": bson.M{"$in": itemIDs}, "revoked_at": nil,
			},
			bson.M{"$set": bson.M{
				"status":                "refunded",
				"revoked_at":            now,
				"timestamps.updated_at": now,
			}},
		); err != nil {
			log.Printf("[stripe webhook] refund: revoke enrollments: %v", err)
		}
		// Mirror the revoke into non-course product types (services, coaching).
		// downloads/newsletter unwind via badge removal below.
		for _, item := range items {
			revokeProductEntitlements(tenantID, contact.Id, item.ProductID, offerID, item.Id)
		}
	}

	// Strip granted badges from the contact so the library Re-renders with
	// no access to the refunded course's content.
	otherCompleted, _ := db.GetCollection(pkgmodels.PurchaseCollection).Find(bson.M{
		"_id": bson.M{"$ne": purchase.Id}, "tenant_id": tenantID,
		"contact_id": purchase.ContactID, "offer_snapshot.offer_id": offerID,
		"status": pkgmodels.PurchaseStatusCompleted,
	}).Count()
	if otherCompleted == 0 && len(purchase.OfferSnapshot.GrantedBadges) > 0 {
		var badgeIDs []bson.ObjectId
		var badges []pkgmodels.Badge
		_ = db.GetCollection(pkgmodels.BadgeCollection).Find(bson.M{
			"tenant_id": tenantID,
			"name":      bson.M{"$in": purchase.OfferSnapshot.GrantedBadges},
		}).All(&badges)
		for _, b := range badges {
			badgeIDs = append(badgeIDs, b.Id)
		}
		for _, id := range badgeIDs {
			_ = badgecmd.Remove(tenantID, contact.Id, id, "refund", charge.ID, "system")
		}
	}

	// Revoke the authoritative Access Grants for this contact + offer products
	// (COM-CC-014). Under ACCESS_GRANTS_ONLY this is what actually removes
	// Library access; otherwise it keeps the grant ledger consistent with the
	// badge revocation above. Idempotent — already-revoked grants stay revoked.
	if len(itemIDs) > 0 {
		_, _ = db.GetCollection(pkgmodels.AccessGrantCollection).UpdateAll(
			bson.M{
				"tenant_id": tenantID, "purchase_item_id": bson.M{"$in": itemIDs},
				"status": bson.M{"$ne": pkgmodels.GrantStatusRevoked},
			},
			bson.M{"$set": bson.M{"status": pkgmodels.GrantStatusRevoked, "timestamps.updated_at": now}},
		)
	}

	// Mark the immutable Purchase + PurchaseItems refunded (status history, not
	// deletion — the records stay for revenue/audit).
	_ = db.GetCollection(pkgmodels.PurchaseCollection).UpdateId(
		purchase.Id,
		bson.M{"$set": bson.M{"status": pkgmodels.PurchaseStatusRefunded, "timestamps.updated_at": now}},
	)
	_, _ = db.GetCollection(pkgmodels.PurchaseItemCollection).UpdateAll(
		bson.M{"tenant_id": tenantID, "purchase_id": purchase.Id, "status": bson.M{"$ne": pkgmodels.ItemStatusRefunded}},
		bson.M{"$set": bson.M{"status": pkgmodels.ItemStatusRefunded, "timestamps.updated_at": now}},
	)

	if purchase.StripeSubscriptionID != "" {
		_ = db.GetCollection(pkgmodels.RecurringAgreementCollection).Update(
			bson.M{"tenant_id": tenantID, "stripe_subscription_id": purchase.StripeSubscriptionID},
			bson.M{"$set": bson.M{"status": "refunded", "timestamps.updated_at": now}},
		)
	}

	// Mark the matching PurchaseLog row(s) as refunded so revenue queries
	// exclude them. Matched primarily by charge id (Stripe-supplied) and
	// secondarily by (tenant, contact, offer) for charges whose original
	// PurchaseLog stored only the session id.
	purchaseFilter := bson.M{
		"tenant_id": tenantID,
		"user_id":   contact.Id,
		"offer_id":  offerID,
		"status":    bson.M{"$ne": "refunded"},
	}
	if charge.PaymentIntent != "" {
		purchaseFilter = bson.M{
			"tenant_id":        tenantID,
			"stripe_charge_id": charge.PaymentIntent,
			"status":           bson.M{"$ne": "refunded"},
		}
	} else if purchase.StripeSessionID != "" {
		purchaseFilter = bson.M{"tenant_id": tenantID, "stripe_charge_id": purchase.StripeSessionID, "status": bson.M{"$ne": "refunded"}}
	}
	_, _ = db.GetCollection(pkgmodels.PurchaseLogCollection).UpdateAll(
		purchaseFilter,
		bson.M{"$set": bson.M{"status": "refunded", "timestamps.updated_at": now}},
	)
	// ANA-005: a refund is a separate immutable fact per refunded log row,
	// never a mutation of the sale fact (idempotent on replays). Re-query
	// with the same filter shape, now matching status refunded.
	refundedFilter := bson.M{}
	for k, v := range purchaseFilter {
		refundedFilter[k] = v
	}
	refundedFilter["status"] = "refunded"
	var refunded []pkgmodels.PurchaseLog
	_ = db.GetCollection(pkgmodels.PurchaseLogCollection).Find(refundedFilter).All(&refunded)
	for i := range refunded {
		analytics.WriteRefundFact(&refunded[i])
	}

	log.Printf("[stripe webhook] refund: revoked offer %s for contact %s (charge %s)",
		offerHex, contact.Id.Hex(), charge.ID)
	return nil
}

// upgradeNewsletterSubscriptionForOffer flips the contact's newsletter
// subscription to a paid tier when the purchased offer is bound to a
// NewsletterTier. Idempotent: re-runs upgrade an existing row without
// duplicating, and is safe to call when the offer is not bound to any
// newsletter (no-op).
func upgradeNewsletterSubscriptionForOffer(tenantID, contactID bson.ObjectId, email string, offerID bson.ObjectId, stripeSubscriptionID string) error {
	// Find every newsletter that has this offer wired to a tier. A single
	// offer COULD theoretically grant access to several newsletters, so we
	// upgrade them all.
	var products []pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"tenant_id":                 tenantID,
		"product_type":              pkgmodels.ProductTypeNewsletter,
		"newsletter.tiers.offer_id": offerID,
	}).All(&products); err != nil {
		return err
	}
	now := time.Now()
	for _, p := range products {
		if p.Newsletter == nil {
			continue
		}
		// Resolve the tier id within this newsletter.
		tierIDHex := pkgmodels.NewsletterFreeTierID
		for _, t := range p.Newsletter.Tiers {
			if t.OfferID == offerID {
				tierIDHex = t.Id.Hex()
				break
			}
		}

		col := db.GetCollection(pkgmodels.NewsletterSubscriptionCollection)
		var existing pkgmodels.NewsletterSubscription
		findErr := col.Find(bson.M{
			"tenant_id":  tenantID,
			"product_id": p.Id,
			"contact_id": contactID,
		}).One(&existing)
		if findErr == nil {
			_ = col.Update(bson.M{"_id": existing.Id}, bson.M{"$set": bson.M{
				"status":                 pkgmodels.NewsletterSubscriptionStatusActive,
				"tier_id":                tierIDHex,
				"offer_id":               offerID,
				"stripe_subscription_id": stripeSubscriptionID,
				"confirmed_at":           now,
				"opt_in_token":           "",
			}})
			continue
		}
		// No existing row — paid checkout was the first interaction. Create
		// it as already-active (paid checkout is implicit consent).
		newSub := pkgmodels.NewNewsletterSubscription(tenantID, p.Id, contactID, strings.ToLower(strings.TrimSpace(email)), tierIDHex)
		newSub.Status = pkgmodels.NewsletterSubscriptionStatusActive
		newSub.OfferID = offerID
		newSub.StripeSubscriptionID = stripeSubscriptionID
		newSub.ConfirmedAt = &now
		newSub.UnsubscribeToken = bson.NewObjectId().Hex()
		if err := col.Insert(newSub); err != nil {
			log.Printf("[stripe webhook] newsletter sub insert failed: %v", err)
		}
	}
	return nil
}
