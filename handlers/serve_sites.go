package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/site"
	"github.com/josephalai/sentanyl/pkg/auth"
)

// RegisterSiteRoutes registers all website builder API routes.
func RegisterSiteRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.POST("/sites", handleCreateSite)
	tenantAPI.GET("/sites", handleListSites)
	tenantAPI.GET("/sites/:siteId", handleGetSite)
	tenantAPI.PUT("/sites/:siteId", handleUpdateSite)
	tenantAPI.DELETE("/sites/:siteId", handleDeleteSite)
	tenantAPI.POST("/sites/:siteId/domains/:domainId/attach", handleAttachDomain)
	tenantAPI.GET("/sites/components/registry", handleComponentRegistry)
}

func handleCreateSite(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req site.SiteCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	s, err := site.ServiceCreateSite(req, tenantID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"status": "ok", "site": s})
}

func handleListSites(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	sites, err := site.ServiceListSites(tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list sites"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "sites": sites})
}

func handleGetSite(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	siteID := c.Param("siteId")
	if !bson.IsObjectIdHex(siteID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid site id"})
		return
	}
	s, err := site.ServiceGetSite(bson.ObjectIdHex(siteID), tenantID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "site not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "site": s})
}

func handleUpdateSite(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	siteID := c.Param("siteId")
	if !bson.IsObjectIdHex(siteID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid site id"})
		return
	}
	var req site.SiteUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if err := site.ServiceUpdateSite(bson.ObjectIdHex(siteID), tenantID, req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleDeleteSite(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	siteID := c.Param("siteId")
	if !bson.IsObjectIdHex(siteID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid site id"})
		return
	}
	if err := site.ServiceDeleteSite(bson.ObjectIdHex(siteID), tenantID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete site"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleAttachDomain(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	siteID := c.Param("siteId")
	if !bson.IsObjectIdHex(siteID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid site id"})
		return
	}
	domainID := c.Param("domainId")
	var req struct {
		Domain string `json:"domain"`
	}
	_ = c.ShouldBindJSON(&req)
	// Use domainId from URL path if present (and not "_"), otherwise use body domain string.
	domainRef := domainID
	if domainRef == "" || domainRef == "_" {
		domainRef = req.Domain
	}
	if domainRef == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "domain id or hostname is required"})
		return
	}
	if err := site.ServiceAttachDomain(bson.ObjectIdHex(siteID), tenantID, domainRef); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleComponentRegistry(c *gin.Context) {
	defs := site.GetAllComponentDefs()
	c.JSON(http.StatusOK, gin.H{"status": "ok", "components": defs})
}
