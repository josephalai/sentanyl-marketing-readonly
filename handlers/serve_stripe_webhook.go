package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/email"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// RegisterStripeWebhookRoute registers the public Stripe webhook receiver.
// Stripe calls this URL for every tenant using a platform-wide endpoint with
// ?tenant_id=<hex> to dispatch into the correct tenant's webhook secret.
func RegisterStripeWebhookRoute(publicAPI *gin.RouterGroup) {
	publicAPI.POST("/stripe/webhook", handleStripeWebhook)
}

// Tolerance for Stripe timestamp skew, matching the Stripe SDK default.
const stripeSignatureTolerance = 5 * time.Minute

// stripeEvent is the minimal shape we care about.
type stripeEvent struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Data struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

// stripeCheckoutSession is the subset of Session fields we use.
type stripeCheckoutSession struct {
	ID              string            `json:"id"`
	Mode            string            `json:"mode"`
	CustomerEmail   string            `json:"customer_email"`
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
	ID               string            `json:"id"`
	Status           string            `json:"status"`
	Metadata         map[string]string `json:"metadata"`
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

	if err := verifyStripeSignature(c.GetHeader("Stripe-Signature"), rawBody, tenant.StripeWebhookSecret); err != nil {
		log.Printf("[stripe webhook] signature verify failed: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid signature"})
		return
	}

	var evt stripeEvent
	if err := json.Unmarshal(rawBody, &evt); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}

	switch evt.Type {
	case "checkout.session.completed":
		if err := processCheckoutSessionCompleted(tenantID, &tenant, evt.Data.Object); err != nil {
			log.Printf("[stripe webhook] checkout.session.completed: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	case "invoice.paid":
		if err := processInvoicePaid(tenantID, evt.Data.Object); err != nil {
			log.Printf("[stripe webhook] invoice.paid: %v", err)
		}
	case "customer.subscription.deleted", "customer.subscription.updated":
		if err := processSubscriptionStateChange(tenantID, evt.Data.Object); err != nil {
			log.Printf("[stripe webhook] %s: %v", evt.Type, err)
		}
	case "charge.refunded", "charge.refund.updated":
		if err := processChargeRefunded(tenantID, evt.Data.Object); err != nil {
			log.Printf("[stripe webhook] %s: %v", evt.Type, err)
		}
	default:
		// Acknowledge unhandled events so Stripe stops retrying.
	}

	c.JSON(http.StatusOK, gin.H{"received": true})
}

// verifyStripeSignature checks a Stripe-Signature header against the request body.
// Format: "t=<timestamp>,v1=<sig>,v1=<sig>..."; we verify at least one v1 signature.
func verifyStripeSignature(header string, body []byte, secret string) error {
	if header == "" {
		return fmt.Errorf("missing signature header")
	}
	var tsStr string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			tsStr = kv[1]
		case "v1":
			sigs = append(sigs, kv[1])
		}
	}
	if tsStr == "" || len(sigs) == 0 {
		return fmt.Errorf("malformed signature header")
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return fmt.Errorf("bad timestamp")
	}
	if age := time.Since(time.Unix(ts, 0)); age > stripeSignatureTolerance || age < -stripeSignatureTolerance {
		return fmt.Errorf("timestamp outside tolerance")
	}

	signedPayload := tsStr + "." + string(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	expected := hex.EncodeToString(mac.Sum(nil))

	for _, s := range sigs {
		if hmac.Equal([]byte(expected), []byte(s)) {
			return nil
		}
	}
	return fmt.Errorf("no matching signature")
}

func processCheckoutSessionCompleted(tenantID bson.ObjectId, tenant *pkgmodels.Tenant, raw json.RawMessage) error {
	var session stripeCheckoutSession
	if err := json.Unmarshal(raw, &session); err != nil {
		return fmt.Errorf("decode session: %w", err)
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

	if err := grantOfferBadges(tenantID, contact.Id, offer.GrantedBadges); err != nil {
		return fmt.Errorf("grant badges: %w", err)
	}

	if err := recordSubscription(tenantID, contact.Id, offer.Id, session.ID, session.Subscription); err != nil {
		return fmt.Errorf("record subscription: %w", err)
	}

	// Enroll the contact in every product the offer includes. Try them all
	// (a single bad product shouldn't block the rest), but if any fail we
	// return an error so Stripe retries delivery AND the failure is visible
	// in the Stripe dashboard's webhook log. The lms-service enroll handler
	// is idempotent on (tenant, contact, product), so retries are safe.
	var enrollFailures []string
	for _, productID := range offer.IncludedProducts {
		if err := callInternalEnroll(tenantID, contact.Id, productID); err != nil {
			log.Printf("[stripe webhook] ENROLL FAILED tenant=%s offer=%s product=%s contact=%s email=%s: %v",
				tenantID.Hex(), offer.Id.Hex(), productID.Hex(), contact.Id.Hex(), email, err)
			enrollFailures = append(enrollFailures, fmt.Sprintf("%s: %v", productID.Hex(), err))
		}
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
			len(enrollFailures), len(offer.IncludedProducts), offer.Id.Hex(), strings.Join(enrollFailures, "; "))
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
// badge name, then $addToSet them onto the contact's User.Badges array.
func grantOfferBadges(tenantID, contactID bson.ObjectId, badgeNames []string) error {
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
	if len(badgeIDs) == 0 {
		return nil
	}
	return db.GetCollection(pkgmodels.UserCollection).Update(
		bson.M{"_id": contactID},
		bson.M{"$addToSet": bson.M{"badges": bson.M{"$each": badgeIDs}}},
	)
}

// recordSubscription upserts a Subscription row. For recurring payments the
// StripeSubscriptionID is the idempotency key. For one-time payments (no
// subscription id), the (tenant, contact, offer) triple is the idempotency
// key. StripeSessionID is always saved when present so the post-checkout
// landing page can look up provisioning state by session id.
func recordSubscription(tenantID, contactID, offerID bson.ObjectId, stripeSessionID, stripeSubscriptionID string) error {
	col := db.GetCollection(pkgmodels.SubscriptionCollection)
	var existing pkgmodels.Subscription
	filter := bson.M{"tenant_id": tenantID, "contact_id": contactID, "offer_id": offerID}
	if stripeSubscriptionID != "" {
		filter = bson.M{"stripe_subscription_id": stripeSubscriptionID}
	}
	if err := col.Find(filter).One(&existing); err == nil {
		update := bson.M{"status": "active", "timestamps.updated_at": time.Now()}
		if stripeSubscriptionID != "" {
			update["stripe_subscription_id"] = stripeSubscriptionID
		}
		if stripeSessionID != "" {
			update["stripe_session_id"] = stripeSessionID
		}
		return col.Update(bson.M{"_id": existing.Id}, bson.M{"$set": update})
	}
	sub := pkgmodels.NewSubscription(tenantID, contactID, offerID)
	sub.StripeSessionID = stripeSessionID
	sub.StripeSubscriptionID = stripeSubscriptionID
	now := time.Now()
	sub.SoftDeletes.CreatedAt = &now
	return col.Insert(sub)
}

// callInternalEnroll posts to lms-service /internal/enroll. Best-effort; the
// lms-service handler is itself idempotent on (tenant, contact, product).
func callInternalEnroll(tenantID, contactID, productID bson.ObjectId) error {
	lmsURL := os.Getenv("LMS_SERVICE_URL")
	if lmsURL == "" {
		lmsURL = "http://lms-service:8083"
	}
	body, _ := json.Marshal(map[string]string{
		"tenant_id":  tenantID.Hex(),
		"contact_id": contactID.Hex(),
		"product_id": productID.Hex(),
	})
	req, err := http.NewRequest("POST", lmsURL+"/internal/enroll", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
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

func setPasswordResetToken(contactID bson.ObjectId) (string, time.Time, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, err
	}
	token := hex.EncodeToString(buf)
	expires := time.Now().Add(48 * time.Hour)
	err := db.GetCollection(pkgmodels.UserCollection).Update(
		bson.M{"_id": contactID},
		bson.M{"$set": bson.M{
			"password_reset_token":   token,
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
	host := os.Getenv("SMTP_HOST")
	portStr := os.Getenv("SMTP_PORT")
	if host == "" {
		host = "mailhog"
	}
	port := 1025
	if portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}
	return email.NewSMTPProvider(host, port)
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
	return db.GetCollection(pkgmodels.SubscriptionCollection).Update(
		bson.M{"tenant_id": tenantID, "stripe_subscription_id": inv.Subscription},
		bson.M{"$set": bson.M{"status": "active", "timestamps.updated_at": time.Now()}},
	)
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
	return db.GetCollection(pkgmodels.SubscriptionCollection).Update(
		bson.M{"tenant_id": tenantID, "stripe_subscription_id": sub.ID},
		bson.M{"$set": bson.M{"status": newStatus, "timestamps.updated_at": time.Now()}},
	)
}

// stripeCharge is the subset of Charge fields we use for refund handling.
type stripeCharge struct {
	ID                  string            `json:"id"`
	Amount              int64             `json:"amount"`
	AmountRefunded      int64             `json:"amount_refunded"`
	Refunded            bool              `json:"refunded"`
	PaymentIntent       string            `json:"payment_intent"`
	Metadata            map[string]string `json:"metadata"`
	PaymentIntentMeta   map[string]string `json:"payment_intent_metadata,omitempty"`
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
	if err := db.GetCollection(pkgmodels.OfferCollection).FindId(offerID).One(&offer); err != nil {
		return fmt.Errorf("offer %s lookup failed: %w", offerHex, err)
	}

	// Find the contact: prefer email metadata; fall back to the most recent
	// Subscription on this offer if nothing else identifies the buyer.
	var contact pkgmodels.User
	contactErr := error(nil)
	if contactEmail != "" {
		contactErr = db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
			"tenant_id": tenantID,
			"email":     pkgmodels.EmailAddress(contactEmail),
		}).One(&contact)
	}
	if contactErr != nil || contactEmail == "" {
		var sub pkgmodels.Subscription
		err := db.GetCollection(pkgmodels.SubscriptionCollection).Find(bson.M{
			"tenant_id":         tenantID,
			"offer_id":          offerID,
			"stripe_session_id": bson.M{"$ne": ""},
		}).Sort("-timestamps.created_at").One(&sub)
		if err != nil {
			return fmt.Errorf("could not resolve refund contact for offer %s", offerHex)
		}
		if err := db.GetCollection(pkgmodels.UserCollection).FindId(sub.ContactID).One(&contact); err != nil {
			return fmt.Errorf("contact %s missing", sub.ContactID.Hex())
		}
	}

	now := time.Now()

	// Revoke every enrollment for this contact + offer-included products.
	if len(offer.IncludedProducts) > 0 {
		if _, err := db.GetCollection(pkgmodels.CourseEnrollmentCollection).UpdateAll(
			bson.M{
				"tenant_id":  tenantID,
				"contact_id": contact.Id,
				"product_id": bson.M{"$in": offer.IncludedProducts},
				"revoked_at": nil,
			},
			bson.M{"$set": bson.M{
				"status":                "refunded",
				"revoked_at":            now,
				"timestamps.updated_at": now,
			}},
		); err != nil {
			log.Printf("[stripe webhook] refund: revoke enrollments: %v", err)
		}
	}

	// Strip granted badges from the contact so the library Re-renders with
	// no access to the refunded course's content.
	if len(offer.GrantedBadges) > 0 {
		var badgeIDs []bson.ObjectId
		var badges []pkgmodels.Badge
		_ = db.GetCollection(pkgmodels.BadgeCollection).Find(bson.M{
			"tenant_id": tenantID,
			"name":      bson.M{"$in": offer.GrantedBadges},
		}).All(&badges)
		for _, b := range badges {
			badgeIDs = append(badgeIDs, b.Id)
		}
		if len(badgeIDs) > 0 {
			_ = db.GetCollection(pkgmodels.UserCollection).Update(
				bson.M{"_id": contact.Id},
				bson.M{"$pull": bson.M{"badges": bson.M{"$in": badgeIDs}}},
			)
		}
	}

	// Mark the matching subscription record as refunded.
	_, _ = db.GetCollection(pkgmodels.SubscriptionCollection).UpdateAll(
		bson.M{
			"tenant_id":  tenantID,
			"contact_id": contact.Id,
			"offer_id":   offerID,
			"status":     bson.M{"$ne": "refunded"},
		},
		bson.M{"$set": bson.M{"status": "refunded", "timestamps.updated_at": now}},
	)

	log.Printf("[stripe webhook] refund: revoked offer %s for contact %s (charge %s)",
		offerHex, contact.Id.Hex(), charge.ID)
	return nil
}
