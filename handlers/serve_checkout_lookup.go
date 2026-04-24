package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/site"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
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

	// Resolve the tenant from the hostname.  Try attached-domain lookup first
	// (prod path), then the *.site.lvh.me dev pattern via the shared site
	// resolver — which is the same logic used to serve the website itself.
	var tenantID bson.ObjectId
	var tenantDomain pkgmodels.TenantDomain
	if err := db.GetCollection(pkgmodels.DomainCollection).Find(bson.M{
		"hostname":              hostname,
		"timestamps.deleted_at": nil,
	}).One(&tenantDomain); err == nil {
		tenantID = tenantDomain.TenantID
	} else if s, err := site.FindSiteByDomain(hostname); err == nil {
		tenantID = s.TenantID
	} else {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain not found"})
		return
	}

	var sub pkgmodels.Subscription
	err := db.GetCollection(pkgmodels.SubscriptionCollection).Find(bson.M{
		"tenant_id":         tenantID,
		"stripe_session_id": sessionID,
	}).One(&sub)
	if err != nil {
		// Webhook hasn't landed yet (or this session_id doesn't belong to us).
		c.JSON(http.StatusOK, checkoutLookupResponse{Status: "processing"})
		return
	}

	var contact pkgmodels.User
	if err := db.GetCollection(pkgmodels.UserCollection).FindId(sub.ContactID).One(&contact); err != nil {
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

	// New buyer: return the reset token as long as it's still valid. Expired
	// token (past 48h) falls through to the "check email" path.
	tokenValid := contact.PasswordResetToken != "" &&
		contact.PasswordResetExpires != nil &&
		time.Now().Before(*contact.PasswordResetExpires)
	if !tokenValid {
		c.JSON(http.StatusOK, checkoutLookupResponse{
			Status: "existing_account",
			Email:  string(contact.Email),
		})
		return
	}

	c.JSON(http.StatusOK, checkoutLookupResponse{
		Status:     "new_account",
		Email:      string(contact.Email),
		SetupToken: contact.PasswordResetToken,
	})
}
