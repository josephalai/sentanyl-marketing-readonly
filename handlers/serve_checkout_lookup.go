package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/site"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/publicchannel"
)

// RegisterCheckoutLookupRoute registers the public post-checkout lookup
// endpoint. Called by /portal/welcome to determine what to do with the buyer
// after they return from Stripe: send first-time buyers to the password-setup
// page with their token, and returning buyers to the login page with a
// purchase-complete flash.
func RegisterCheckoutLookupRoute(publicAPI *gin.RouterGroup) {
	publicAPI.GET("/checkout/lookup", handleCheckoutLookup)
}

type checkoutLookupResponse struct {
	Status     string `json:"status"`
	Email      string `json:"email,omitempty"`
	SetupToken string `json:"setup_token,omitempty"`
}

func handleCheckoutLookup(c *gin.Context) {
	sessionID := c.Query("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id required"})
		return
	}

	hostname := c.Request.Host
	if forwarded := c.GetHeader("X-Forwarded-Host"); forwarded != "" {
		hostname = forwarded
	}

	// Resolve the tenant from the hostname. Prefer the shared resolver
	// (verified tenant_domains → active frontend channel → published-site
	// fallback); keep the legacy unverified tenant_domains lookup as a last
	// resort so existing installs without is_verified don't break.
	var tenantID bson.ObjectId
	if pubCtx, err := publicchannel.ResolvePublicRequestWithDomain(c, hostname); err == nil {
		tenantID = pubCtx.TenantID
	} else {
		var tenantDomain pkgmodels.TenantDomain
		if err := db.GetCollection(pkgmodels.DomainCollection).Find(bson.M{
			"hostname":              hostname,
			"timestamps.deleted_at": nil,
		}).One(&tenantDomain); err == nil {
			log.Printf("checkout lookup: resolved %s via UNVERIFIED tenant_domains row — verify this domain", hostname)
			tenantID = tenantDomain.TenantID
		} else if s, err := site.FindSiteByDomain(hostname); err == nil {
			tenantID = s.TenantID
		} else {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain not found"})
			return
		}
	}

	var purchase pkgmodels.Purchase
	err := db.GetCollection(pkgmodels.PurchaseCollection).Find(bson.M{
		"tenant_id":         tenantID,
		"stripe_session_id": sessionID,
	}).One(&purchase)
	if err != nil {
		// Webhook hasn't landed yet (or this session_id doesn't belong to us).
		c.JSON(http.StatusOK, checkoutLookupResponse{Status: "processing"})
		return
	}

	var contact pkgmodels.User
	if err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{"_id": purchase.ContactID, "tenant_id": tenantID}).One(&contact); err != nil {
		c.JSON(http.StatusOK, checkoutLookupResponse{Status: "processing"})
		return
	}

	// Returning buyer: already has a password. Send them to the login page.
	if contact.PasswordHash != "" {
		c.JSON(http.StatusOK, checkoutLookupResponse{
			Status: "existing_account",
			Email:  string(contact.Email),
		})
		return
	}

	// New buyer: only a hash of the setup token is stored (ID-015), so the
	// stored value can't be echoed back. Mint a fresh token for this success-
	// page visit instead — the plaintext handoff happens exactly once per
	// mint, and re-minting invalidates any earlier emailed link (same
	// last-request-wins semantics as the forgot-password flow).
	token, _, err := setPasswordResetToken(contact.Id)
	if err != nil {
		log.Printf("[checkout lookup] setup-token mint for %s: %v", contact.Email, err)
		c.JSON(http.StatusOK, checkoutLookupResponse{
			Status: "existing_account",
			Email:  string(contact.Email),
		})
		return
	}

	c.JSON(http.StatusOK, checkoutLookupResponse{
		Status:     "new_account",
		Email:      string(contact.Email),
		SetupToken: token,
	})
}
