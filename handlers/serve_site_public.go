package handlers

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/checkout"
	"github.com/josephalai/sentanyl/marketing-service/internal/forms"
	"github.com/josephalai/sentanyl/marketing-service/internal/site"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/publicchannel"
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
// submissions from published website pages. Fields holds arbitrary
// FormField values (keyed by FieldName) so the executor can apply each
// FormField.MapsTo declaration without a fixed schema. The flat name/email/
// phone keys remain for backwards compatibility with browser-form posts that
// don't know about Fields.
type publicFormRequest struct {
	Domain  string            `json:"domain" form:"domain"`
	Name    string            `json:"name" form:"name"`
	Email   string            `json:"email" form:"email"`
	Phone   string            `json:"phone" form:"phone"`
	Message string            `json:"message" form:"message"`
	FormID  string            `json:"form_id" form:"form_id"`
	NextURL string            `json:"next_url" form:"next_url"`
	// HoneypotField ("website") must stay empty — a real user never sees or
	// fills it; a bot that auto-fills every field trips it (ACQ-009).
	HoneypotField string           `json:"website" form:"website"`
	Fields        map[string]string `json:"fields" form:"-"`
	// VideoSessionID is set by the sentanyl-video.js fetch shim when the
	// form lives on a page with an active video session. It propagates
	// from form submit → MediaLeadCapture row + (when an offer is also
	// attached) Stripe metadata → PurchaseLog.VideoSessionPublicId, so
	// conversions trace back to the video that drove them.
	VideoSessionID string `json:"video_session_id" form:"video_session_id"`
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
		// Pull every form-encoded key the binder didn't claim into Fields so
		// the executor can resolve FormField.MapsTo by FieldName.
		if req.Fields == nil {
			req.Fields = map[string]string{}
		}
		for k, vs := range c.Request.PostForm {
			if len(vs) == 0 {
				continue
			}
			switch k {
			case "domain", "name", "email", "phone", "message", "form_id", "next_url":
				continue
			}
			// Repeated keys (multiselect checkboxes) join comma-separated —
			// the executor's multiselect coercion splits on commas.
			req.Fields[k] = strings.Join(vs, ",")
		}
	}

	// Resolve domain from body, header, or Host. Used as the fallback tenant
	// resolver when no form_id is provided (legacy lead-capture path).
	domain := req.Domain
	if domain == "" {
		domain = c.GetHeader("X-Forwarded-Host")
	}
	if domain == "" {
		domain = c.Request.Host
	}

	// New path — caller supplies form_id. The form record carries TenantID,
	// FormField definitions, and the OnSubmit action chain. We resolve the
	// form, validate required fields, run the executor, and return its
	// structured Result.
	if strings.TrimSpace(req.FormID) != "" {
		// Tenant-scope the form lookup by request domain. Public public_ids
		// are not a trust boundary: a tenant's form_id must only resolve on
		// that tenant's own domains.
		tenantID, err := site.ResolveTenantFromDomain(domain)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "site not found"})
			return
		}
		runFormSubmission(c, tenantID, req.FormID, &req, strings.Contains(contentType, "application/json"), "builder_page")
		return
	}

	// Legacy path — no form_id. Resolve tenant by domain and do a basic
	// upsert. Preserves the prior behavior for published websites that
	// haven't been re-published with form_id wiring yet.
	if strings.TrimSpace(req.Email) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email is required"})
		return
	}
	s, err := site.FindSiteByDomain(domain)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "site not found"})
		return
	}
	contact, err := upsertContact(s.TenantID, req.Email, req.Name, req.Phone, req.Message)
	if err != nil {
		log.Printf("form submit: failed to upsert contact: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save contact"})
		return
	}
	if req.NextURL != "" {
		c.Redirect(http.StatusSeeOther, req.NextURL)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":     "ok",
		"contact_id": contact.Id.Hex(),
	})
}

// runFormSubmission is the shared post-resolution form path: loads the form
// by public_id scoped to the resolved tenant, validates required fields, runs
// the OnSubmit executor, and writes the response (303 redirect for browser
// posts, JSON otherwise). Shared by the legacy builder route and the
// /api/public/forms/:formId channel route.
func runFormSubmission(c *gin.Context, tenantID bson.ObjectId, formPublicID string, req *publicFormRequest, isJSON bool, source string) {
	var form pkgmodels.PageForm
	if err := db.GetCollection(pkgmodels.PageFormCollection).Find(bson.M{
		"public_id":             formPublicID,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&form); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "form not found"})
		return
	}

	// ACQ-009 spam protection: a filled honeypot means a bot. Ack with 200 so
	// the bot can't distinguish acceptance from rejection, but do nothing.
	if strings.TrimSpace(req.HoneypotField) != "" {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}

	values := mergeFormValues(req)
	if missing := validateRequiredFields(&form, values); len(missing) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":          "missing required fields",
			"missing_fields": missing,
		})
		return
	}
	// ACQ-006: format-validate an email field when present.
	if em := strings.TrimSpace(values["email"]); em != "" && !looksLikeEmail(em) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email address"})
		return
	}

	result := forms.Execute(&form, forms.Submission{
		FieldValues:          values,
		Source:               source,
		VideoSessionPublicId: req.VideoSessionID,
	})

	// Browser form posts that include a redirect get a 303 to the
	// configured URL; the next_url body field still wins over the
	// stored OnSubmit.RedirectURL so handcrafted forms can override.
	redirect := strings.TrimSpace(req.NextURL)
	if redirect == "" {
		redirect = result.RedirectURL
	}
	if redirect != "" && !isJSON {
		c.Redirect(http.StatusSeeOther, redirect)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":            "ok",
		"contact_id":        result.ContactID,
		"contact_public_id": result.ContactPublicID,
		"redirect_url":      redirect,
		"downloads":         result.Downloads,
		"badges_assigned":   result.BadgesAssigned,
		"badges_removed":    result.BadgesRemoved,
		"lists_added":       result.ListsAdded,
		"lists_removed":     result.ListsRemoved,
		"stories_started":   result.StoriesStarted,
		"products_granted":  result.ProductsGranted,
		"offer_attached":    result.OfferAttached,
		"warnings":          result.Warnings,
	})
}

// looksLikeEmail is a cheap RFC-shaped sanity check (not full RFC 5322):
// exactly one @, a dotted domain, no spaces (ACQ-006).
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at != strings.LastIndexByte(s, '@') || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	domain := s[at+1:]
	return strings.Contains(domain, ".") && !strings.HasPrefix(domain, ".") && !strings.HasSuffix(domain, ".")
}

// mergeFormValues collapses the flat name/email/phone/message fields and the
// arbitrary Fields map into a single FieldName→value lookup the action
// executor consumes.
func mergeFormValues(req *publicFormRequest) map[string]string {
	values := map[string]string{}
	for k, v := range req.Fields {
		if v != "" {
			values[k] = v
		}
	}
	if req.Email != "" {
		values["email"] = req.Email
	}
	if req.Name != "" {
		values["name"] = req.Name
	}
	if req.Phone != "" {
		values["phone"] = req.Phone
	}
	if req.Message != "" {
		values["message"] = req.Message
	}
	return values
}

// validateRequiredFields returns the FieldNames the form marked Required
// that were not provided in the submission.
func validateRequiredFields(form *pkgmodels.PageForm, values map[string]string) []string {
	var missing []string
	for _, f := range form.Fields {
		if f == nil || !f.Required {
			continue
		}
		if v, ok := values[f.FieldName]; !ok || strings.TrimSpace(v) == "" {
			missing = append(missing, f.FieldName)
		}
	}
	return missing
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
	// VideoSessionID propagates from the runtime sentanyl-video.js fetch
	// shim when the buyer is mid-watch on the same page; it crosses to
	// Stripe via session metadata so the webhook handler can stamp it on
	// the resulting PurchaseLog.
	VideoSessionID string `json:"video_session_id" form:"video_session_id"`
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

	// Resolve the tenant. The shared resolver checks verified tenant_domains
	// and active frontend channels first, then falls back to the published
	// builder Site — so builder pages keep working, and a verified domain
	// with no published Site (BYO coded website) can now start checkout.
	pubCtx, err := publicchannel.ResolvePublicRequestWithDomain(c, req.Domain)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "site not found"})
		return
	}
	if err := publicchannel.EnforceOrigin(pubCtx); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	domain = pubCtx.Domain

	// Fetch the offer.
	var offer pkgmodels.Offer
	err = db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
		"_id":                   bson.ObjectIdHex(req.OfferID),
		"tenant_id":             pubCtx.TenantID,
		"timestamps.deleted_at": nil,
	}).One(&offer)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "offer not found"})
		return
	}

	startCheckoutSession(c, pubCtx, &offer, req.Email, req.SuccessURL, req.CancelURL, req.VideoSessionID)
}

// startCheckoutSession runs the shared post-resolution checkout flow:
// duplicate-purchase guard, success/cancel URL defaulting (request body →
// channel defaults → portal welcome), tenant Stripe credential selection,
// and Stripe Checkout session creation. Shared by the legacy builder route
// and the /api/public/checkout/:offerId channel route.
func startCheckoutSession(c *gin.Context, pubCtx *publicchannel.PublicRequestContext, offer *pkgmodels.Offer, email, successURL, cancelURL, videoSessionID string) {
	tenantID := pubCtx.TenantID
	domain := pubCtx.Domain

	// COM-CC-008: only a published offer is purchasable. Draft offers aren't
	// live yet; archived offers are retired (existing access preserved, no
	// new checkouts). Blank status = legacy live offer.
	if offer.Status == pkgmodels.OfferStatusDraft || offer.Status == pkgmodels.OfferStatusArchived {
		c.JSON(http.StatusConflict, gin.H{"error": "this offer is not available for purchase"})
		return
	}

	// Duplicate-course guard: if the buyer already owns every product in
	// this offer (via a prior purchase), don't start a second Stripe charge.
	// The returned redirect sends them to the portal login, where a flash
	// tells them they already have access.
	if email := strings.ToLower(strings.TrimSpace(email)); email != "" {
		if alreadyOwnsAllProductsInOffer(tenantID, email, offer) {
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
	// {CHECKOUT_SESSION_ID} into the URL it redirects to. Channel-level
	// defaults sit between the request body and the hardcoded fallback.
	if successURL == "" && pubCtx.Channel != nil {
		successURL = pubCtx.Channel.DefaultSuccessURL
	}
	if successURL == "" {
		successURL = "/portal/welcome?session_id={CHECKOUT_SESSION_ID}"
	}
	if cancelURL == "" && pubCtx.Channel != nil {
		cancelURL = pubCtx.Channel.DefaultCancelURL
	}
	if cancelURL == "" {
		cancelURL = "/"
	}

	// Resolve the tenant's Stripe credentials. Tenants may be configured
	// either by direct API-key entry (StripeSecretKey) or via Stripe Connect
	// (StripeConnectAccountID + platform secret). Manual keys take precedence
	// so a tenant can override without disconnecting.
	var tenant pkgmodels.Tenant
	err := db.GetCollection(pkgmodels.TenantCollection).FindId(tenantID).One(&tenant)
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
	stripeKey := utils.DecryptSecret(tenant.StripeSecretKey)
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
	extras := map[string]string{}
	if v := strings.TrimSpace(videoSessionID); v != "" {
		extras["video_session_id"] = v
	}
	stripeSessionURL, err := checkout.CreateStripeCheckoutSessionWithExtras(stripeKey, stripeAcct, offer, tenantID, successURL, cancelURL, domain, strings.TrimSpace(email), extras)
	if err != nil {
		log.Printf("Stripe checkout session creation failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create checkout session"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":          "ok",
		"checkout_url":    stripeSessionURL,
		"offer_id":        offer.Id.Hex(),
		"offer_public_id": offer.PublicId,
		"title":           offer.Title,
		"amount":          offer.Amount,
		"currency":        offer.Currency,
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

// CreateStripeCheckoutSession is retained as a thin wrapper around
// internal/checkout.CreateStripeCheckoutSession. The implementation moved
// out of handlers so the routes package (newsletter customer upgrade)
// can call it without an import cycle.
func CreateStripeCheckoutSession(stripeKey, stripeAccount string, offer *pkgmodels.Offer, tenantID bson.ObjectId, successURL, cancelURL, domain, customerEmail string) (string, error) {
	return checkout.CreateStripeCheckoutSession(stripeKey, stripeAccount, offer, tenantID, successURL, cancelURL, domain, customerEmail)
}
