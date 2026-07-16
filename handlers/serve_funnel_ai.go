package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/josephalai/sentanyl/marketing-service/internal/ai"
	"github.com/josephalai/sentanyl/marketing-service/internal/funnel"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"gopkg.in/mgo.v2/bson"
)

// RegisterFunnelAIRoutes wires AI funnel generation from template.
func RegisterFunnelAIRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.POST("/funnel-ai/generate-from-template", GovernAI("funnel.generate", 4096), handleAIGenerateFunnelFromTemplate)
	tenantAPI.GET("/funnel-ai/templates", handleListFunnelTemplatesForAI)
	tenantAPI.GET("/funnel-ai/default-template", handleGetDefaultTemplate)
	tenantAPI.POST("/funnel-ai/materialize", handleMaterializeFunnelTemplate)
}

// handleGetDefaultTemplate returns the default template the tenant should
// use for a given template_kind. Resolution order:
//  1. tenant-marked default (FunnelTemplate.DefaultForTenant=true matching kind)
//  2. system-marked default (DefaultForPageType matching kind, tenant_id=system)
//  3. first-imported template of that kind, scoped to tenant or system
//
// Returns 404 only when the corpus is fully empty for that kind. Tenants
// hitting "Generate from template" without choosing one go through here.
func handleGetDefaultTemplate(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	kind := strings.TrimSpace(c.Query("kind"))
	if kind == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind query param required"})
		return
	}
	col := db.GetCollection(pkgmodels.FunnelTemplateCollection)

	// Tenant-marked default first.
	var t pkgmodels.FunnelTemplate
	if err := col.Find(bson.M{
		"tenant_id":             tenantID,
		"template_kind":         kind,
		"default_for_tenant":    true,
		"timestamps.deleted_at": nil,
	}).One(&t); err == nil {
		c.JSON(http.StatusOK, t)
		return
	}

	// System default — DefaultForPageType matching kind across any tenant.
	if err := col.Find(bson.M{
		"template_kind":         kind,
		"default_for_page_type": kind,
		"timestamps.deleted_at": nil,
	}).One(&t); err == nil {
		c.JSON(http.StatusOK, t)
		return
	}

	// Last resort: any template of that kind scoped to tenant, then any tenant.
	if err := col.Find(bson.M{
		"tenant_id":             tenantID,
		"template_kind":         kind,
		"timestamps.deleted_at": nil,
	}).One(&t); err == nil {
		c.JSON(http.StatusOK, t)
		return
	}
	if err := col.Find(bson.M{
		"template_kind":         kind,
		"timestamps.deleted_at": nil,
	}).One(&t); err == nil {
		c.JSON(http.StatusOK, t)
		return
	}

	c.JSON(http.StatusNotFound, gin.H{"error": "no template available for kind " + kind})
}

// handleMaterializeFunnelTemplate turns AI slot output + structured inputs +
// a target (domain + path + optional form) into a saved FunnelPage with its
// Funnel→Route→Stage parent chain. Returns the resulting URL the page will
// be served at by the existing site renderer.
func handleMaterializeFunnelTemplate(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req struct {
		TemplateID       string                 `json:"template_id" binding:"required"`
		LLMOutput        map[string]interface{} `json:"llm_output"`
		StructuredInputs map[string]interface{} `json:"structured_inputs"`
		FormID           string                 `json:"form_id"`
		DomainID         string                 `json:"domain_id"`
		Hostname         string                 `json:"hostname"`
		Path             string                 `json:"path"`
		Publish          bool                   `json:"publish"`
		Name             string                 `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var tmpl pkgmodels.FunnelTemplate
	// Tenant-scoped first; fall back to system templates so the seeded
	// corpus (loaded with the system tenant id) materializes for every
	// tenant. Mirrors handleGetDefaultTemplate's resolution order.
	err := db.GetCollection(pkgmodels.FunnelTemplateCollection).Find(bson.M{
		"public_id":             req.TemplateID,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&tmpl)
	if err != nil {
		if err = db.GetCollection(pkgmodels.FunnelTemplateCollection).Find(bson.M{
			"public_id":             req.TemplateID,
			"timestamps.deleted_at": nil,
		}).One(&tmpl); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "template not found"})
			return
		}
	}
	res, err := funnel.Materialize(tenantID, funnel.MaterializeRequest{
		Template:         &tmpl,
		LLMOutput:        req.LLMOutput,
		StructuredInputs: req.StructuredInputs,
		FormPublicID:     req.FormID,
		DomainID:         req.DomainID,
		Hostname:         req.Hostname,
		Path:             req.Path,
		Publish:          req.Publish,
		Name:             req.Name,
	})
	if err != nil {
		log.Printf("materialize: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":           "ok",
		"page_id":          res.PageID,
		"page_public_id":   res.PagePublicID,
		"funnel_id":        res.FunnelID,
		"funnel_public_id": res.FunnelPublicID,
		"url":              res.URL,
	})
}

// handleListFunnelTemplatesForAI returns templates with their slot manifests — used by the AI Architect picker.
func handleListFunnelTemplatesForAI(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var templates []pkgmodels.FunnelTemplate
	// The caller's own templates plus the curated Shared system corpus (the
	// "beat Kajabi" starter gallery, visible to every tenant).
	db.GetCollection(pkgmodels.FunnelTemplateCollection).Find(bson.M{
		"$or": []bson.M{
			{"tenant_id": tenantID},
			{"shared": true},
		},
		"timestamps.deleted_at": nil,
	}).All(&templates)
	if templates == nil {
		templates = []pkgmodels.FunnelTemplate{}
	}
	c.JSON(http.StatusOK, templates)
}

// handleAIGenerateFunnelFromTemplate generates slot content for a chosen template.
func handleAIGenerateFunnelFromTemplate(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		TemplateID   string   `json:"template_id" binding:"required"`
		Instruction  string   `json:"instruction" binding:"required"`
		ContextPacks []string `json:"context_pack_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Load template — tenant-owned first, then the Shared system corpus so the
	// curated gallery generates for every tenant (mirrors the materialize
	// fallback in handleMaterializeFunnelTemplate).
	var tmpl pkgmodels.FunnelTemplate
	col := db.GetCollection(pkgmodels.FunnelTemplateCollection)
	if err := col.Find(bson.M{
		"public_id": req.TemplateID,
		"tenant_id": tenantID,
	}).One(&tmpl); err != nil {
		if err = col.Find(bson.M{
			"public_id": req.TemplateID,
			"shared":    true,
		}).One(&tmpl); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "template not found"})
			return
		}
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

	chunks := resolveContextPacks(tenantID, req.ContextPacks)
	brandProfile := resolveBrandProfile(tenantID)

	// Build slot-aware prompt and run it through JSON mode. The prompt fully
	// specifies the {slotKey: value} object; GenerateJSON returns it verbatim
	// (GenerateEmail would coerce it into email-shaped fields and drop it).
	prompt := buildFunnelSlotPrompt(req.Instruction, tmpl, chunks, brandProfile)
	raw, err := provider.GenerateJSON(ai.GenerateTextRequest{
		Ctx:       aiRequestContext(c),
		Prompt:    prompt,
		MaxTokens: 4096,
	})
	if err != nil {
		log.Printf("[funnel-ai] generation error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI generation failed: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"template_id":   req.TemplateID,
		"template_name": tmpl.Name,
		"slot_content":  raw,
	})
}

func buildFunnelSlotPrompt(instruction string, tmpl pkgmodels.FunnelTemplate, contextChunks []string, brandProfile string) string {
	var sb strings.Builder

	// MasterPrompt comes first so importer-supplied per-template framing
	// ("you are filling a webinar registration page in a soft, expert tone…")
	// sets the LLM's voice before generic instructions kick in. Falls back
	// to the generic copywriter framing when the template doesn't carry one.
	if mp := strings.TrimSpace(tmpl.MasterPrompt); mp != "" {
		sb.WriteString(mp)
		sb.WriteString("\n\n")
	} else {
		sb.WriteString("You are an expert conversion copywriter filling in content for a pre-designed funnel page template.\n\n")
	}

	// StyleProfile gives the LLM concrete style anchors derived from the
	// extracted template (tone, visual style, copy style, CTA style,
	// audience). Skipped when the manifest didn't extract any.
	if sp := tmpl.StyleProfile; sp != nil {
		var styleParts []string
		if sp.Tone != "" {
			styleParts = append(styleParts, "tone: "+sp.Tone)
		}
		if sp.VisualStyle != "" {
			styleParts = append(styleParts, "visual: "+sp.VisualStyle)
		}
		if sp.CopyStyle != "" {
			styleParts = append(styleParts, "copy: "+sp.CopyStyle)
		}
		if sp.CTAStyle != "" {
			styleParts = append(styleParts, "CTA: "+sp.CTAStyle)
		}
		if sp.AudienceAssumption != "" {
			styleParts = append(styleParts, "audience: "+sp.AudienceAssumption)
		}
		if len(styleParts) > 0 {
			sb.WriteString("STYLE PROFILE: ")
			sb.WriteString(strings.Join(styleParts, "; "))
			sb.WriteString("\n\n")
		}
	}

	if brandProfile != "" {
		sb.WriteString("BRAND PROFILE:\n")
		sb.WriteString(brandProfile)
		sb.WriteString("\n\n")
	}

	if len(contextChunks) > 0 {
		sb.WriteString("REFERENCE MATERIAL:\n")
		for i, chunk := range contextChunks {
			if i > 0 {
				sb.WriteString("\n---\n")
			}
			sb.WriteString(chunk)
		}
		sb.WriteString("\n\n")
	}

	sb.WriteString(fmt.Sprintf("TEMPLATE: %s\n\n", tmpl.Name))

	if tmpl.SlotManifest != nil && len(tmpl.SlotManifest.Slots) > 0 {
		sb.WriteString("SLOTS TO FILL:\n")
		hasSentanylSlot := false
		for _, slot := range tmpl.SlotManifest.Slots {
			sb.WriteString(fmt.Sprintf("- %s (%s): %s", slot.Key, slot.Label, slot.SlotType))
			if slot.MaxWords > 0 {
				sb.WriteString(fmt.Sprintf(", max %d words", slot.MaxWords))
			}
			if slot.Constraints != "" {
				sb.WriteString(fmt.Sprintf(", constraints: %s", slot.Constraints))
			}
			if slot.Description != "" {
				sb.WriteString(fmt.Sprintf(" — %s", slot.Description))
			}
			sb.WriteString("\n")
			if strings.HasPrefix(slot.SlotType, "sentanyl_") {
				hasSentanylSlot = true
			}
		}
		sb.WriteString("\n")
		sb.WriteString("Return a JSON object where each key is a slot key and the value is the generated content.\n")
		sb.WriteString("For 'array' slots, the value should be a JSON array of objects.\n\n")

		// Phase 11B Step 8: when the template's manifest references the
		// new Sentanyl-aware slot types (video / squeeze / sales) OR
		// the template_kind is one of the video kinds, surface the
		// resolution rules so the LLM knows it must return tenant
		// resource IDs (Media.public_id, PageForm.public_id,
		// Offer.public_id) rather than free-form HTML.
		kind := strings.ToLower(strings.TrimSpace(tmpl.TemplateKind))
		if hasSentanylSlot || kind == "video_sales_letter" || kind == "video_squeeze" {
			sb.WriteString("SENTANYL RESOURCE SLOTS:\n")
			sb.WriteString("- sentanyl_video_id: must resolve to a Media.public_id from the tenant. Pick the most recent or most-relevant media. NEVER invent an id; if no media exists, return an empty string and add a note in `_warnings`.\n")
			sb.WriteString("- sentanyl_squeeze_form_id: must resolve to a PageForm.public_id whose on_submit grants a free product (newsletter, lead magnet). Prefer the form named most recently or one whose name matches the page intent.\n")
			sb.WriteString("- sentanyl_sales_offer_id: must resolve to an Offer.public_id appropriate to the page's pitch — match by price tier or offer name when the user gave a hint.\n")
			sb.WriteString("- sentanyl_media_poster_url (optional): direct URL to the poster image for the chosen media.\n")
			sb.WriteString("These slot values get injected into the augmented `<video data-sentanyl>` block that the runtime player auto-activates; downstream conversion attribution depends on them being correct.\n\n")
		}
	} else {
		sb.WriteString("The template uses {{ render_blocks }} for content insertion.\n")
		sb.WriteString("Return a JSON object with key 'content' containing the HTML to inject.\n\n")
	}

	// ExpectedOutputSchema is shipped to the LLM verbatim when the importer
	// carried one — it's the strongest typing signal we have for output
	// shape and overrides the generic "key=slot" instruction.
	if len(tmpl.ExpectedOutputSchema) > 0 {
		if b, err := json.Marshal(tmpl.ExpectedOutputSchema); err == nil {
			sb.WriteString("EXPECTED OUTPUT SCHEMA (return JSON conforming to this):\n")
			sb.Write(b)
			sb.WriteString("\n\n")
		}
	}

	sb.WriteString("TASK:\n")
	sb.WriteString(instruction)

	return sb.String()
}
