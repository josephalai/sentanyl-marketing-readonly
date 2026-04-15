package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/ai"
	"github.com/josephalai/sentanyl/marketing-service/internal/site"
	"github.com/josephalai/sentanyl/pkg/auth"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterSiteAIRoutes registers AI generation/editing routes.
func RegisterSiteAIRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.POST("/sites/:siteId/ai-generate", handleAIGenerateSite)
	tenantAPI.POST("/sites/:siteId/pages/:pageId/ai-generate", handleAIGeneratePage)
	tenantAPI.POST("/sites/:siteId/pages/:pageId/ai-edit", handleAIEditPage)
	tenantAPI.POST("/sites/:siteId/pages/:pageId/patch", handlePatchPage)
}

func handleAIGenerateSite(c *gin.Context) {
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

	provider, err := ai.GetConfiguredProvider()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if provider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI provider not configured"})
		return
	}

	var req ai.SiteGenerationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	result, err := provider.GenerateSite(req)
	if err != nil {
		log.Printf("AI site generation failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI generation failed"})
		return
	}

	// Apply generated data to the site.
	siteObjID := bson.ObjectIdHex(siteID)
	updates := bson.M{}
	if result.SiteName != "" {
		updates["name"] = result.SiteName
	}
	if result.Theme != "" {
		updates["theme"] = result.Theme
	}
	if result.SEO != nil {
		updates["seo"] = pkgmodels.SEOConfig{
			MetaTitle:       result.SEO.MetaTitle,
			MetaDescription: result.SEO.MetaDescription,
		}
	}
	if result.Navigation != nil {
		nav := pkgmodels.NavigationConfig{}
		for _, link := range result.Navigation.HeaderLinks {
			nav.HeaderNavLinks = append(nav.HeaderNavLinks, pkgmodels.NavLink{
				Label: link.Label,
				URL:   link.URL,
			})
		}
		for _, link := range result.Navigation.FooterLinks {
			nav.FooterNavLinks = append(nav.FooterNavLinks, pkgmodels.NavLink{
				Label: link.Label,
				URL:   link.URL,
			})
		}
		updates["navigation"] = nav
	}
	if len(updates) > 0 {
		_ = site.UpdateSite(siteObjID, tenantID, updates)
	}

	// Create pages from the AI result.
	var createdPages []site.SitePage
	for _, pageResult := range result.Pages {
		page := site.NewSitePage(pageResult.Name, pageResult.Slug, siteObjID, tenantID)
		page.IsHome = pageResult.IsHome
		if pageResult.SEO != nil {
			page.SEO = &pkgmodels.SEOConfig{
				MetaTitle:       pageResult.SEO.MetaTitle,
				MetaDescription: pageResult.SEO.MetaDescription,
			}
		}
		page.DraftDocument = pageResult.PuckRoot
		if err := site.CreateSitePage(page); err != nil {
			log.Printf("Failed to create AI-generated page %s: %v", pageResult.Name, err)
			continue
		}
		createdPages = append(createdPages, *page)
	}

	c.JSON(http.StatusOK, gin.H{
		"status":        "ok",
		"site":          result,
		"pages_created": len(createdPages),
	})
}

func handleAIGeneratePage(c *gin.Context) {
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

	provider, err := ai.GetConfiguredProvider()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if provider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI provider not configured"})
		return
	}

	var req struct {
		Prompt string `json:"prompt" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "prompt is required"})
		return
	}

	doc, err := provider.GeneratePage(req.Prompt)
	if err != nil {
		log.Printf("AI page generation failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI generation failed"})
		return
	}

	// Save the generated document as the draft.
	if err := site.ServiceSaveDocument(bson.ObjectIdHex(pageID), tenantID, doc); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save generated document"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "document": doc})
}

func handleAIEditPage(c *gin.Context) {
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

	provider, err := ai.GetConfiguredProvider()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if provider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI provider not configured"})
		return
	}

	var req struct {
		Instruction string `json:"instruction" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "instruction is required"})
		return
	}

	// Get current document.
	currentDoc, err := site.ServiceGetDocument(bson.ObjectIdHex(pageID), tenantID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "page not found"})
		return
	}

	editReq := ai.PageEditRequest{
		Instruction:     req.Instruction,
		CurrentDocument: currentDoc,
	}
	result, err := provider.EditPage(editReq)
	if err != nil {
		log.Printf("AI page edit failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI edit failed"})
		return
	}

	// Save the updated document.
	if err := site.ServiceSaveDocument(bson.ObjectIdHex(pageID), tenantID, result.UpdatedDocument); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save edited document"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"document": result.UpdatedDocument,
		"summary":  result.Summary,
	})
}

func handlePatchPage(c *gin.Context) {
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

	var patches site.PatchDocument
	if err := c.ShouldBindJSON(&patches); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid patch document"})
		return
	}
	if len(patches.Operations) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no operations provided"})
		return
	}

	// Get current document.
	currentDoc, err := site.ServiceGetDocument(bson.ObjectIdHex(pageID), tenantID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "page not found"})
		return
	}
	if currentDoc == nil {
		currentDoc = map[string]any{"content": []any{}, "root": map[string]any{"props": map[string]any{}}}
	}

	// Apply patches.
	updatedDoc, err := site.ApplyPatches(currentDoc, patches)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Save the patched document.
	if err := site.ServiceSaveDocument(bson.ObjectIdHex(pageID), tenantID, updatedDoc); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save patched document"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"document": updatedDoc,
	})
}
