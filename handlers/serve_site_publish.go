package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/site"
	"github.com/josephalai/sentanyl/pkg/auth"
)

// RegisterSitePublishRoutes registers preview/publish routes.
func RegisterSitePublishRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.POST("/sites/:siteId/pages/:pageId/preview", handlePreviewPage)
	tenantAPI.POST("/sites/:siteId/pages/:pageId/publish", handlePublishPage)
}

// RegisterPublicSiteRoutes registers the public website delivery endpoint.
// This serves published HTML snapshots for designated website hosts.
func RegisterPublicSiteRoutes(publicAPI *gin.RouterGroup) {
	publicAPI.GET("/site/page", handlePublicSitePage)
}

func handlePreviewPage(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	pageID := c.Param("pageId")
	if !bson.IsObjectIdHex(pageID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid page id"})
		return
	}
	html, err := site.ServicePreviewPage(bson.ObjectIdHex(pageID), tenantID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "html": html})
}

func handlePublishPage(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	pageID := c.Param("pageId")
	if !bson.IsObjectIdHex(pageID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid page id"})
		return
	}
	html, err := site.ServicePublishPage(bson.ObjectIdHex(pageID), tenantID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "html": html})
}

// handlePublicSitePage serves published website pages by domain and path.
// This is called via Caddy for designated website hosts only.
func handlePublicSitePage(c *gin.Context) {
	domain := c.Query("domain")
	path := c.Query("path")
	if domain == "" {
		domain = c.GetHeader("X-Forwarded-Host")
	}
	if domain == "" {
		domain = c.Request.Host
	}
	if path == "" {
		path = "/"
	}

	html, err := site.ServiceResolvePublicPage(domain, path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "page not found"})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, html)
}
