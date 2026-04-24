package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/site"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// RegisterPublicFormRoutes registers public-facing form submission and checkout
// routes on the marketing API group. These are unauthenticated — they resolve
// the site/tenant from the submitted domain context.
func RegisterPublicFormRoutes(publicAPI *gin.RouterGroup) {
	publicAPI.POST("/site/form/submit", handlePublicFormSubmit)
	publicAPI.POST("/site/checkout/start", handlePublicCheckoutStart)
}

// ---------- Public Form Submission ----------

// publicFormRequest is the expected JSON or form body for lead/contact form
// submissions from published website pages.
type publicFormRequest struct {
	Domain  string `json:"domain" form:"domain"`
	Name    string `json:"name" form:"name"`
	Email   string `json:"email" form:"email"`
	Phone   string `json:"phone" form:"phone"`
	Message string `json:"message" form:"message"`
	FormID  string `json:"form_id" form:"form_id"`
	NextURL string `json:"next_url" form:"next_url"`
}

func handlePublicFormSubmit(c *gin.Context) {
	var req publicFormRequest

	// Support both JSON and form-encoded submissions.
	contentType := c.GetHeader("Content-Type")
	if strings.Contains(contentType, "application/json") {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
	} else {
		if err := c.ShouldBind(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid form data"})
			return
		}
	}

	// Resolve domain from body, header, or Host.
	domain := req.Domain
	if domain == "" {
		domain = c.GetHeader("X-Forwarded-Host")
	}
	if domain == "" {
		domain = c.Request.Host
	}

	if strings.TrimSpace(req.Email) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email is required"})
		return
	}

	// Resolve the site to find the tenant.
	s, err := site.FindSiteByDomain(domain)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "site not found"})
		return
	}

	// Upsert contact in the tenant's user/contact collection.
	contact, err := upsertContact(s.TenantID, req.Email, req.Name, req.Phone, req.Message)
	if err != nil {
		log.Printf("form submit: failed to upsert contact: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save contact"})
		return
	}

	// If a redirect URL is configured, redirect (for browser form submissions).
	if req.NextURL != "" {
		c.Redirect(http.StatusSeeOther, req.NextURL)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":     "ok",
		"contact_id": contact.Id.Hex(),
	})
}

// upsertContact finds or creates a contact (User) in the tenant's user
// collection keyed by email address.
func upsertContact(tenantID bson.ObjectId, email, name, phone, message string) (*pkgmodels.User, error) {
	col := db.GetCollection(pkgmodels.UserCollection)
	var existing pkgmodels.User

	err := col.Find(bson.M{
		"email":     strings.ToLower(strings.TrimSpace(email)),
		"tenant_id": tenantID,
	}).One(&existing)

	if err == nil {
		// Update existing contact if new fields are provided.
		updates := bson.M{}
		if name != "" {
			parts := strings.SplitN(name, " ", 2)
			updates["name.first_name"] = parts[0]
			if len(parts) > 1 {
				updates["name.last_name"] = parts[1]
			}
		}
		if phone != "" {
			updates["phone_number"] = phone
		}
		if len(updates) > 0 {
			now := time.Now()
			updates["timestamps.updated_at"] = now
			_ = col.Update(bson.M{"_id": existing.Id}, bson.M{"$set": updates})
		}
		return &existing, nil
	}

	// Create new contact.
	now := time.Now()
	firstName := ""
	lastName := ""
	if name != "" {
		parts := strings.SplitN(name, " ", 2)
		firstName = parts[0]
		if len(parts) > 1 {
			lastName = parts[1]
		}
	}

	newContact := pkgmodels.User{
		Id:       bson.NewObjectId(),
		PublicId: utils.GeneratePublicId(),
		TenantID: tenantID,
		Email:    pkgmodels.EmailAddress(strings.ToLower(strings.TrimSpace(email))),
	}
	newContact.Name.First = firstName
	newContact.Name.Last = lastName
	newContact.Phone = phone
	newContact.SoftDeletes.CreatedAt = &now

	if err := col.Insert(newContact); err != nil {
		return nil, err
	}
	return &newContact, nil
}

// ---------- Public Checkout Start ----------

type checkoutStartRequest struct {
	Domain     string `json:"domain" form:"domain"`
	OfferID    string `json:"offer_id" form:"offer_id"`
	Email      string `json:"email" form:"email"`
	SuccessURL string `json:"success_url" form:"success_url"`
	CancelURL  string `json:"cancel_url" form:"cancel_url"`
}

func handlePublicCheckoutStart(c *gin.Context) {
	var req checkoutStartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	// Resolve domain.
	domain := req.Domain
	if domain == "" {
		domain = c.GetHeader("X-Forwarded-Host")
	}
	if domain == "" {
		domain = c.Request.Host
	}

	if req.OfferID == "" || !bson.IsObjectIdHex(req.OfferID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid offer_id is required"})
		return
	}

	// Resolve the site to find the tenant.
	s, err := site.FindSiteByDomain(domain)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "site not found"})
		return
	}

	// Fetch the offer.
	var offer pkgmodels.Offer
	err = db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
		"_id":                   bson.ObjectIdHex(req.OfferID),
		"tenant_id":             s.TenantID,
		"timestamps.deleted_at": nil,
	}).One(&offer)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "offer not found"})
		return
	}

	// Duplicate-course guard: if the buyer already owns every product in
	// this offer (via a prior purchase), don't start a second Stripe charge.
	// The returned redirect sends them to the portal login, where a flash
	// tells them they already have access.
	if email := strings.ToLower(strings.TrimSpace(req.Email)); email != "" {
		if alreadyOwnsAllProductsInOffer(s.TenantID, email, &offer) {
			scheme := "https"
			if strings.Contains(domain, "lvh.me") || strings.Contains(domain, "localhost") {
				scheme = "http"
			}
			c.JSON(http.StatusConflict, gin.H{
				"status":       "already_purchased",
				"message":      "You already have access to every product in this offer.",
				"redirect_url": fmt.Sprintf("%s://%s/portal/login?already_purchased=true&email=%s", scheme, domain, url.QueryEscape(email)),
				"email":        email,
			})
			return
		}
	}

	// Default to our Welcome landing page which polls the checkout lookup
	// endpoint and routes the buyer to set-password (new account) or login
	// (returning buyer) without needing to check email. Stripe substitutes
	// {CHECKOUT_SESSION_ID} into the URL it redirects to.
	successURL := req.SuccessURL
	if successURL == "" {
		successURL = "/portal/welcome?session_id={CHECKOUT_SESSION_ID}"
	}
	cancelURL := req.CancelURL
	if cancelURL == "" {
		cancelURL = "/"
	}

	// Resolve the tenant's Stripe credentials. Tenants may be configured
	// either by direct API-key entry (StripeSecretKey) or via Stripe Connect
	// (StripeConnectAccountID + platform secret). Manual keys take precedence
	// so a tenant can override without disconnecting.
	var tenant pkgmodels.Tenant
	err = db.GetCollection(pkgmodels.TenantCollection).FindId(s.TenantID).One(&tenant)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":    "payment processing is not configured for this business",
			"offer_id": offer.Id.Hex(),
			"title":    offer.Title,
			"amount":   offer.Amount,
			"currency": offer.Currency,
		})
		return
	}
	stripeKey := tenant.StripeSecretKey
	stripeAcct := ""
	if stripeKey == "" && tenant.StripeConnectAccountID != "" {
		stripeKey = os.Getenv("STRIPE_PLATFORM_SECRET_KEY")
		stripeAcct = tenant.StripeConnectAccountID
	}
	if stripeKey == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":    "payment processing is not configured for this business",
			"offer_id": offer.Id.Hex(),
			"title":    offer.Title,
			"amount":   offer.Amount,
			"currency": offer.Currency,
		})
		return
	}

	// Create a Stripe Checkout Session using the tenant's Stripe key.
	stripeSessionURL, err := createStripeCheckoutSession(stripeKey, stripeAcct, &offer, s.TenantID, successURL, cancelURL, domain, strings.TrimSpace(req.Email))
	if err != nil {
		log.Printf("Stripe checkout session creation failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create checkout session"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":       "ok",
		"checkout_url": stripeSessionURL,
		"offer_id":     offer.Id.Hex(),
		"title":        offer.Title,
		"amount":       offer.Amount,
		"currency":     offer.Currency,
	})
}

// alreadyOwnsAllProductsInOffer reports whether the contact identified by
// email already has access (via a granted badge on any prior purchase) to
// every product included in this offer. Used by checkout-start to prevent a
// buyer from accidentally paying twice for the same course.
//
// Access resolution mirrors handleGetLibraryProducts: the contact's Badges
// are joined to Offer.GrantedBadges by name, and the union of those offers'
// IncludedProducts forms the owned-set.
func alreadyOwnsAllProductsInOffer(tenantID bson.ObjectId, email string, offer *pkgmodels.Offer) bool {
	if len(offer.IncludedProducts) == 0 {
		return false
	}
	var contact pkgmodels.User
	err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
		"email":                 pkgmodels.EmailAddress(email),
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&contact)
	if err != nil {
		return false
	}
	if len(contact.Badges) == 0 {
		return false
	}
	var badgeNames []string
	for _, badgeID := range contact.Badges {
		var b pkgmodels.Badge
		if err := db.GetCollection(pkgmodels.BadgeCollection).FindId(badgeID).One(&b); err == nil {
			badgeNames = append(badgeNames, b.Name)
		}
	}
	if len(badgeNames) == 0 {
		return false
	}
	var ownedOffers []pkgmodels.Offer
	_ = db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"granted_badges":        bson.M{"$in": badgeNames},
		"timestamps.deleted_at": nil,
	}).All(&ownedOffers)
	owned := make(map[bson.ObjectId]bool, len(ownedOffers)*2)
	for _, o := range ownedOffers {
		for _, pid := range o.IncludedProducts {
			owned[pid] = true
		}
	}
	for _, pid := range offer.IncludedProducts {
		if !owned[pid] {
			return false
		}
	}
	return true
}

// createStripeCheckoutSession creates a Stripe Checkout Session via the API.
// Uses the tenant's own Stripe secret key. Metadata (tenant_id, offer_id,
// domain) is attached to the session and mirrored into payment_intent_data and
// subscription_data so it survives across Stripe's internal objects and is
// available to the Stripe webhook handler.
func createStripeCheckoutSession(stripeKey, stripeAccount string, offer *pkgmodels.Offer, tenantID bson.ObjectId, successURL, cancelURL, domain, customerEmail string) (string, error) {
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

	// Session-level metadata is what the webhook reads. Mirror into
	// payment_intent_data so it survives onto the PaymentIntent too. Stripe
	// rejects subscription_data unless mode=subscription, so we omit it for
	// now; add a branch when subscription-mode offers are supported.
	form.Set("metadata[tenant_id]", tenantID.Hex())
	form.Set("metadata[offer_id]", offer.Id.Hex())
	form.Set("metadata[domain]", domain)
	form.Set("payment_intent_data[metadata][tenant_id]", tenantID.Hex())
	form.Set("payment_intent_data[metadata][offer_id]", offer.Id.Hex())
	form.Set("payment_intent_data[metadata][domain]", domain)

	httpReq, err := http.NewRequest("POST", "https://api.stripe.com/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.SetBasicAuth(stripeKey, "")
	if stripeAccount != "" {
		// Direct charge on a connected account. Stripe routes the charge to
		// this account and webhooks fire against that account's endpoint.
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
