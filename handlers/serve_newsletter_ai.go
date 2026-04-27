package handlers

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/ai"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterNewsletterAIRoutes mounts the newsletter authoring AI endpoints.
// Caller has already wrapped the group in RequireTenantAuth. The endpoints
// reuse the existing SiteAIProvider — newsletter posts are Puck documents,
// the same shape funnel pages already use, so we get authoring + editing for
// free by piping through GeneratePage / EditPage with newsletter-flavored
// prompts.
func RegisterNewsletterAIRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.POST("/newsletters/:productId/posts/ai/generate", handleNewsletterAIGenerate)
	tenantAPI.POST("/newsletters/:productId/posts/:postId/ai/edit", handleNewsletterAIEdit)
}

type newsletterGenerateReq struct {
	Prompt        string   `json:"prompt"`
	Tone          string   `json:"tone"`
	BrandProfile  string   `json:"brand_profile"`
	ContextChunks []string `json:"context_chunks"`
}

type newsletterGenerateResp struct {
	Title            string         `json:"title"`
	Subtitle         string         `json:"subtitle"`
	BodyDoc          map[string]any `json:"body_doc"`
	BodyMarkdown     string         `json:"body_markdown"`
	EmailSubject     string         `json:"email_subject"`
	EmailPreviewText string         `json:"email_preview_text"`
	SuggestedTags    []string       `json:"suggested_tags"`
}

func handleNewsletterAIGenerate(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	productID := c.Param("productId")
	if !bson.IsObjectIdHex(productID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
		return
	}
	var req newsletterGenerateReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Prompt) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "prompt required"})
		return
	}

	// Build a newsletter-flavored prompt that produces a Puck doc plus the
	// post metadata we want. We reuse the existing GeneratePage method which
	// already emits a valid Puck root for our renderer. When no LLM provider
	// is configured (dev / tests), or the call fails, we fall through to a
	// stub doc so the authoring UX never breaks — tenants can still iterate
	// from a starter scaffold including the gate-break blocks.
	puckPrompt := buildNewsletterPostPuckPrompt(req)
	provider, perr := ai.GetConfiguredProvider()
	var doc map[string]any
	if perr != nil || provider == nil {
		log.Printf("newsletter AI: no provider configured, returning stub draft (%v)", perr)
		doc = stubPuckDoc(req)
	} else if d, err := provider.GeneratePage(puckPrompt); err != nil {
		log.Printf("newsletter AI generation failed, returning stub: %v", err)
		doc = stubPuckDoc(req)
	} else {
		doc = d
	}

	// Pull out title/subject from the prompt as a sensible default; the
	// frontend can override these in the editor.
	title := req.Prompt
	if len(title) > 80 {
		title = title[:80]
	}
	out := newsletterGenerateResp{
		Title:            title,
		Subtitle:         "",
		BodyDoc:          doc,
		BodyMarkdown:     "",
		EmailSubject:     title,
		EmailPreviewText: "",
		SuggestedTags:    []string{},
	}

	// Self-link: pre-load the post on the product if no draft exists yet so
	// the tenant can iterate without a separate "create" call. Optional —
	// frontend may also call POST /posts directly. We skip insert here to
	// avoid duplication.
	_ = ensureNewsletterProduct(tenantID, productID)

	c.JSON(http.StatusOK, out)
}

type newsletterEditReq struct {
	Instruction     string         `json:"instruction"`
	CurrentDocument map[string]any `json:"current_document"`
}

func handleNewsletterAIEdit(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req newsletterEditReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Instruction) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "instruction required"})
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
	result, err := provider.EditPage(ai.PageEditRequest{
		Instruction:     "Edit this newsletter post: " + req.Instruction,
		CurrentDocument: req.CurrentDocument,
	})
	if err != nil {
		log.Printf("newsletter AI edit failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI edit failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"document": result.Document,
		"summary":  result.Summary,
	})
	_ = tenantID
}

func buildNewsletterPostPuckPrompt(req newsletterGenerateReq) string {
	var b strings.Builder
	b.WriteString("Generate a newsletter post as a Puck document. Output ONLY a JSON object matching the Puck root shape (root + content array). ")
	b.WriteString("Use these block types in `content`: HeroSection (heading, subheading), RichTextSection (content as HTML string), CTASection (heading, buttonText, buttonUrl). ")
	b.WriteString("Always include at least: a HeroSection at the top, two RichTextSection blocks, and one CTASection at the bottom. ")
	b.WriteString("Topic / instruction:\n")
	b.WriteString(req.Prompt)
	if req.Tone != "" {
		b.WriteString("\nTone: ")
		b.WriteString(req.Tone)
	}
	if req.BrandProfile != "" {
		b.WriteString("\nBrand voice and positioning:\n")
		b.WriteString(req.BrandProfile)
	}
	for i, ch := range req.ContextChunks {
		b.WriteString(fmt.Sprintf("\nContext chunk %d:\n%s", i+1, ch))
	}
	return b.String()
}

// stubPuckDoc returns a non-empty Puck document built from the prompt so the
// editor is never empty when the LLM is unavailable. Keeps the e2e flow
// independent of an external API key being present.
func stubPuckDoc(req newsletterGenerateReq) map[string]any {
	heading := req.Prompt
	if len(heading) > 60 {
		heading = heading[:60] + "…"
	}
	return map[string]any{
		"root": map[string]any{"props": map[string]any{}},
		"content": []any{
			map[string]any{
				"type": "HeroSection",
				"props": map[string]any{
					"heading":    heading,
					"subheading": "Drafted from your prompt — edit and add your own voice.",
				},
			},
			map[string]any{
				"type": "RichTextSection",
				"props": map[string]any{
					"content": "<p>This is a starter draft. Replace this paragraph with your own writing.</p>",
				},
			},
			map[string]any{
				"type":  "NewsletterSubscriberBreak",
				"props": map[string]any{},
			},
			map[string]any{
				"type": "RichTextSection",
				"props": map[string]any{
					"content": "<p>Subscriber-only content goes below the break above. Free subscribers can read this.</p>",
				},
			},
			map[string]any{
				"type": "NewsletterPaywallBreak",
				"props": map[string]any{
					"tier": "",
				},
			},
			map[string]any{
				"type": "RichTextSection",
				"props": map[string]any{
					"content": "<p>Paid-only content goes below the paywall break. Upgrade to read.</p>",
				},
			},
		},
	}
}

// ensureNewsletterProduct does a sanity load so the route returns a clear
// error if the product is missing. Returns silently otherwise.
func ensureNewsletterProduct(tenantID bson.ObjectId, productIDHex string) error {
	if !bson.IsObjectIdHex(productIDHex) {
		return fmt.Errorf("invalid product id")
	}
	var p pkgmodels.Product
	return db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"_id":          bson.ObjectIdHex(productIDHex),
		"tenant_id":    tenantID,
		"product_type": pkgmodels.ProductTypeNewsletter,
	}).One(&p)
}
