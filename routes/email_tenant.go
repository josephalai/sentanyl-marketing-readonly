package routes

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/emailer"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterTenantEmailRoutes registers the authenticated (API key or JWT)
// tenant-scoped send endpoint. The From address must belong to one of the
// tenant's active sending domains.
func RegisterTenantEmailRoutes(rg *gin.RouterGroup) {
	rg.POST("/email", handleTenantSendEmail)
}

func handleTenantSendEmail(c *gin.Context) {
	var req sendEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	fromDomain := emailer.FromDomain(req.From)
	if fromDomain == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid from address"})
		return
	}

	// The From domain must be an active sending domain owned by this tenant
	// (sending_domains scopes tenancy via creator_id = tenant hex).
	n, err := db.GetCollection(pkgmodels.SendingDomainCollection).Find(bson.M{
		"creator_id":            tenantID.Hex(),
		"domain":                fromDomain,
		"status":                pkgmodels.DomainStatusActive,
		"timestamps.deleted_at": nil,
	}).Count()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify sending domain"})
		return
	}
	if n == 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "from domain not authorized for this tenant: " + fromDomain})
		return
	}

	insertAndSendEmail(c, req)
}
