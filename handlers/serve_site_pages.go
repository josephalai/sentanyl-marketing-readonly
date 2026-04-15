package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/site"
	"github.com/josephalai/sentanyl/pkg/auth"
)

// RegisterSitePageRoutes registers page CRUD and document routes.
func RegisterSitePageRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.POST("/sites/:siteId/pages", handleCreatePage)
	tenantAPI.GET("/sites/:siteId/pages", handleListPages)
	tenantAPI.GET("/sites/:siteId/pages/:pageId", handleGetPage)
	tenantAPI.PUT("/sites/:siteId/pages/:pageId", handleUpdatePage)
	tenantAPI.DELETE("/sites/:siteId/pages/:pageId", handleDeletePage)

	// Document save/load
	tenantAPI.GET("/sites/:siteId/pages/:pageId/document", handleGetDocument)
	tenantAPI.PUT("/sites/:siteId/pages/:pageId/document", handleSaveDocument)

	// Versioning
	tenantAPI.GET("/sites/:siteId/pages/:pageId/versions", handleListVersions)
	tenantAPI.POST("/sites/:siteId/pages/:pageId/restore/:versionId", handleRestoreVersion)
}

func handleCreatePage(c *gin.Context) {
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
	var req site.PageCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	page, err := site.ServiceCreatePage(req, bson.ObjectIdHex(siteID), tenantID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"status": "ok", "page": page})
}

func handleListPages(c *gin.Context) {
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
	pages, err := site.ServiceListPages(bson.ObjectIdHex(siteID), tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list pages"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "pages": pages})
}

func handleGetPage(c *gin.Context) {
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
	page, err := site.ServiceGetPage(bson.ObjectIdHex(pageID), tenantID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "page not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "page": page})
}

func handleUpdatePage(c *gin.Context) {
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
	var req site.PageUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if err := site.ServiceUpdatePage(bson.ObjectIdHex(pageID), tenantID, req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleDeletePage(c *gin.Context) {
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
	if err := site.ServiceDeletePage(bson.ObjectIdHex(pageID), tenantID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete page"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleGetDocument(c *gin.Context) {
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
	doc, err := site.ServiceGetDocument(bson.ObjectIdHex(pageID), tenantID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "page not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "document": doc})
}

func handleSaveDocument(c *gin.Context) {
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
	var req struct {
		Document map[string]any `json:"document"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Document == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "document is required"})
		return
	}
	if err := site.ServiceSaveDocument(bson.ObjectIdHex(pageID), tenantID, req.Document); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save document"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleListVersions(c *gin.Context) {
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
	versions, err := site.ListVersionsByPage(bson.ObjectIdHex(pageID), tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list versions"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "versions": versions})
}

func handleRestoreVersion(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	pageID := c.Param("pageId")
	versionID := c.Param("versionId")
	if !bson.IsObjectIdHex(pageID) || !bson.IsObjectIdHex(versionID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := site.ServiceRestoreVersion(bson.ObjectIdHex(pageID), bson.ObjectIdHex(versionID), tenantID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
