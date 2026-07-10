package routes

import (
	"log"
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
		log.Printf("[tenant-email] status=400 reason=invalid_body remote=%s err=%v", c.ClientIP(), err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		log.Printf("[tenant-email] status=401 reason=unauthenticated remote=%s from=%s to=%s", c.ClientIP(), req.From, req.To)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	fromDomain := emailer.FromDomain(req.From)
	if fromDomain == "" {
		log.Printf("[tenant-email] status=400 reason=invalid_from tenant=%s from=%q to=%s", tenantID.Hex(), req.From, req.To)
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
		log.Printf("[tenant-email] status=500 reason=domain_lookup_failed tenant=%s domain=%s err=%v", tenantID.Hex(), fromDomain, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify sending domain"})
		return
	}
	if n == 0 {
		log.Printf("[tenant-email] status=403 reason=domain_not_authorized tenant=%s domain=%s to=%s", tenantID.Hex(), fromDomain, req.To)
		c.JSON(http.StatusForbidden, gin.H{"error": "from domain not authorized for this tenant: " + fromDomain})
		return
	}

	log.Printf("[tenant-email] status=accepted tenant=%s from=%s to=%s subject=%q", tenantID.Hex(), req.From, req.To, req.SubjectLine)
	insertAndSendEmail(c, req)
}
