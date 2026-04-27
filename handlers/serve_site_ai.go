package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/html"
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
	tenantAPI.POST("/sites/:siteId/ai-generate-from-products", handleGenerateFromProducts)
	tenantAPI.GET("/sites/:siteId/suggest-pages", handleSuggestPages)
	tenantAPI.POST("/sites/:siteId/steal-style", handleStealStyle)
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

	req.BusinessContext = fetchSiteAIContext(tenantID, req.ContextChunks)

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
		// Create a draft snapshot version for the generated page.
		version := site.NewSitePageVersion(siteObjID, page.Id, tenantID, site.VersionTypeDraft, 1)
		version.PuckRoot = pageResult.PuckRoot
		version.SEO = page.SEO
		version.Metadata = &site.SiteVersionMetadata{GeneratedBy: "ai-generate"}
		if err := site.CreateSitePageVersion(version); err != nil {
			log.Printf("Failed to create version for AI-generated page %s: %v", pageResult.Name, err)
		} else {
			_ = site.UpdateSitePage(page.Id, tenantID, bson.M{
				"draft_version_id": version.PublicId,
			})
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
		Prompt         string   `json:"prompt" binding:"required"`
		ContextPackIDs []string `json:"context_pack_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "prompt is required"})
		return
	}

	bizCtx := fetchSiteAIContext(tenantID, req.ContextPackIDs)
	doc, err := provider.GeneratePage(ai.SitePageRequest{
		Prompt:          req.Prompt,
		BusinessContext: bizCtx,
	})
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
		Instruction    string   `json:"instruction" binding:"required"`
		ContextPackIDs []string `json:"context_pack_ids"`
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
	if currentDoc == nil {
		currentDoc = map[string]any{"content": []any{}, "root": map[string]any{"props": map[string]any{}}}
	}

	bizCtx := fetchSiteAIContext(tenantID, req.ContextPackIDs)
	editReq := ai.PageEditRequest{
		Instruction:     req.Instruction,
		CurrentDocument: currentDoc,
		BusinessContext: bizCtx,
	}
	result, err := provider.EditPage(editReq)
	if err != nil {
		log.Printf("AI page edit failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI edit failed"})
		return
	}

	if result.Document == nil {
		log.Printf("AI edit returned nil document")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI returned empty document"})
		return
	}

	// Save the AI-returned document as a new draft snapshot.
	if err := site.ServiceSaveDocument(bson.ObjectIdHex(pageID), tenantID, result.Document); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save edited document"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"document": result.Document,
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

func handleGenerateFromProducts(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	provider, err := ai.GetConfiguredProvider()
	if err != nil || provider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI provider not configured"})
		return
	}

	var req struct {
		ProductIDs     []string `json:"product_ids" binding:"required"`
		PageType       string   `json:"page_type"`
		ContextPackIDs []string `json:"context_pack_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.ProductIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "product_ids is required"})
		return
	}
	if req.PageType == "" {
		req.PageType = "sales page"
	}

	products := fetchProductsByIDs(tenantID, req.ProductIDs)
	if len(products) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "no matching products found"})
		return
	}

	productDetails := buildProductDetailsForGeneration(products)
	bizCtx := fetchSiteAIContext(tenantID, req.ContextPackIDs)
	prompt := ai.BuildGenerateFromProductsPrompt(productDetails, req.PageType)

	doc, err := provider.GeneratePage(ai.SitePageRequest{
		Prompt:          prompt,
		BusinessContext: bizCtx,
	})
	if err != nil {
		log.Printf("AI generate-from-products failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI generation failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "document": doc})
}

func handleSuggestPages(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	provider, err := ai.GetConfiguredProvider()
	if err != nil || provider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI provider not configured"})
		return
	}

	summary := buildProductSummaryForSuggest(tenantID)
	suggestions, err := provider.SuggestPages(ai.SitePageSuggestRequest{ProductSummary: summary})
	if err != nil {
		log.Printf("AI suggest-pages failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI suggestion failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "suggestions": suggestions})
}

func handleStealStyle(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	_ = tenantID // used for auth only

	provider, err := ai.GetConfiguredProvider()
	if err != nil || provider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI provider not configured"})
		return
	}

	var req struct {
		URL string `json:"url" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url is required"})
		return
	}

	parsed, err := url.ParseRequestURI(req.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid url"})
		return
	}
	// Block private/internal IPs.
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasPrefix(host, "127.") || strings.HasPrefix(host, "192.168.") ||
		strings.HasPrefix(host, "10.") || strings.HasPrefix(host, "172.") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "private URLs not allowed"})
		return
	}

	cssContent, fetchErr := fetchURLCSS(req.URL)
	if fetchErr != nil || cssContent == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not fetch or parse styles from URL"})
		return
	}

	// Cap CSS to avoid token overflow.
	if len(cssContent) > 8000 {
		cssContent = cssContent[:8000]
	}

	styleJSON, err := provider.GenerateText(ai.GenerateTextRequest{
		Prompt:    fmt.Sprintf("Extract design tokens from this CSS:\n\n%s", cssContent),
		MaxTokens: 300,
	})
	if err != nil {
		log.Printf("steal-style AI extraction failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "style extraction failed"})
		return
	}

	// Extract JSON from response (may be wrapped in markdown fences).
	styleJSON = strings.TrimSpace(styleJSON)
	if idx := strings.Index(styleJSON, "{"); idx > 0 {
		styleJSON = styleJSON[idx:]
	}
	if end := strings.LastIndex(styleJSON, "}"); end >= 0 && end < len(styleJSON)-1 {
		styleJSON = styleJSON[:end+1]
	}

	var proposed pkgmodels.GlobalStyle
	var confidence int
	var raw map[string]any
	if err := json.Unmarshal([]byte(styleJSON), &raw); err == nil {
		if v, ok := raw["primary_color"].(string); ok {
			proposed.PrimaryColor = v
		}
		if v, ok := raw["secondary_color"].(string); ok {
			proposed.SecondaryColor = v
		}
		if v, ok := raw["accent_color"].(string); ok {
			proposed.AccentColor = v
		}
		if v, ok := raw["heading_font"].(string); ok {
			proposed.HeadingFont = v
		}
		if v, ok := raw["body_font"].(string); ok {
			proposed.BodyFont = v
		}
		if v, ok := raw["border_radius"].(string); ok {
			proposed.BorderRadius = v
		}
		if v, ok := raw["button_style"].(string); ok {
			proposed.ButtonStyle = v
		}
		if v, ok := raw["confidence_score"].(float64); ok {
			confidence = int(v)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"status":     "ok",
		"style":      proposed,
		"confidence": confidence,
	})
}

// fetchURLCSS fetches a URL and extracts inline and linked CSS text.
func fetchURLCSS(targetURL string) (string, error) {
	client := &http.Client{Timeout: 10 * 1e9}
	httpReq, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Sentanyl/1.0)")
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB cap
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "style" && n.FirstChild != nil {
				sb.WriteString(n.FirstChild.Data)
				sb.WriteString("\n")
			}
			if n.Data == "link" {
				var rel, href string
				for _, a := range n.Attr {
					if a.Key == "rel" {
						rel = a.Val
					}
					if a.Key == "href" {
						href = a.Val
					}
				}
				if strings.Contains(rel, "stylesheet") && href != "" && sb.Len() < 6000 {
					cssURL := href
					if !strings.HasPrefix(cssURL, "http") {
						base, _ := url.Parse(targetURL)
						ref, _ := url.Parse(href)
						cssURL = base.ResolveReference(ref).String()
					}
					if cssResp, err := client.Get(cssURL); err == nil {
						defer cssResp.Body.Close()
						if cssBytes, err := io.ReadAll(io.LimitReader(cssResp.Body, 200*1024)); err == nil {
							sb.Write(cssBytes)
							sb.WriteString("\n")
						}
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)

	return sb.String(), nil
}
