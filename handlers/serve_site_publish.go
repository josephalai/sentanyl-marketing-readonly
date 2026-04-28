package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/site"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
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

// RegisterSiteViewRoutes registers unauthenticated site-view routes by public ID.
// These let the browser open a site directly without an auth header.
func RegisterSiteViewRoutes(r *gin.Engine) {
	r.GET("/view/sites/:publicId", handleViewSite)
	r.GET("/view/sites/:publicId/*slug", handleViewSite)
}

// handleViewSite serves a site page as raw HTML by public site ID.
// No auth required — only the public_id is exposed, not the ObjectId.
func handleViewSite(c *gin.Context) {
	publicID := c.Param("publicId")
	slug := c.Param("slug")
	if slug == "" || slug == "/" {
		slug = "/"
	}
	slug = strings.TrimRight(slug, "/")
	if slug == "" {
		slug = "/"
	}

	// Look up site by public_id
	var s pkgmodels.Site
	if err := db.GetCollection(pkgmodels.SiteCollection).Find(bson.M{
		"public_id":             publicID,
		"timestamps.deleted_at": nil,
	}).One(&s); err != nil {
		c.String(http.StatusNotFound, "Site not found")
		return
	}

	// Find the matching page
	query := bson.M{
		"site_id":               s.Id,
		"timestamps.deleted_at": nil,
	}
	if slug == "/" {
		query["is_home"] = true
	} else {
		query["slug"] = slug
	}

	var pg site.SitePage
	if err := db.GetCollection(pkgmodels.SitePageCollection).Find(query).One(&pg); err != nil {
		// Fall back to home page
		if err2 := db.GetCollection(pkgmodels.SitePageCollection).Find(bson.M{
			"site_id":               s.Id,
			"is_home":               true,
			"timestamps.deleted_at": nil,
		}).One(&pg); err2 != nil {
			c.String(http.StatusNotFound, "Page not found")
			return
		}
	}

	// Serve PublishedHTML if available (cloned sites), otherwise render from DraftDocument
	if pg.PublishedHTML != "" {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(pg.PublishedHTML))
		return
	}
	if pg.DraftDocument != nil {
		html := site.RenderPuckDocumentToHTML(pg.DraftDocument, pg.SEO, &s)
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
		return
	}
	html := site.RenderStubPage(&pg, &s)
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
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
