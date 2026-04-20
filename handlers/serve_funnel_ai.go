package handlers

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/josephalai/sentanyl/marketing-service/internal/ai"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"gopkg.in/mgo.v2/bson"
)

// RegisterFunnelAIRoutes wires AI funnel generation from template.
func RegisterFunnelAIRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.POST("/funnel-ai/generate-from-template", handleAIGenerateFunnelFromTemplate)
	tenantAPI.GET("/funnel-ai/templates", handleListFunnelTemplatesForAI)
}

// handleListFunnelTemplatesForAI returns templates with their slot manifests — used by the AI Architect picker.
func handleListFunnelTemplatesForAI(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var templates []pkgmodels.FunnelTemplate
	db.GetCollection(pkgmodels.FunnelTemplateCollection).Find(bson.M{
		"tenant_id":             tenantID,
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

	// Load template
	var tmpl pkgmodels.FunnelTemplate
	if err := db.GetCollection(pkgmodels.FunnelTemplateCollection).Find(bson.M{
		"public_id": req.TemplateID,
		"tenant_id": tenantID,
	}).One(&tmpl); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "template not found"})
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

	chunks := resolveContextPacks(tenantID, req.ContextPacks)
	brandProfile := resolveBrandProfile(tenantID)

	// Build slot-aware prompt
	prompt := buildFunnelSlotPrompt(req.Instruction, tmpl, chunks, brandProfile)
	result, err := provider.GenerateEmail(ai.EmailGenerationRequest{
		Instruction:   prompt,
		ContextChunks: nil, // already embedded in prompt
		BrandProfile:  "",
	})
	if err != nil {
		log.Printf("[funnel-ai] generation error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI generation failed: " + err.Error()})
		return
	}

	// result.Body contains the JSON slot map from the LLM
	c.JSON(http.StatusOK, gin.H{
		"template_id":   req.TemplateID,
		"template_name": tmpl.Name,
		"slot_content":  result.Body,
		"summary":       result.Summary,
	})
}

func buildFunnelSlotPrompt(instruction string, tmpl pkgmodels.FunnelTemplate, contextChunks []string, brandProfile string) string {
	var sb strings.Builder

	sb.WriteString("You are an expert conversion copywriter filling in content for a pre-designed funnel page template.\n\n")

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
		for _, slot := range tmpl.SlotManifest.Slots {
			sb.WriteString(fmt.Sprintf("- %s (%s): %s", slot.Key, slot.Label, slot.SlotType))
			if slot.MaxWords > 0 {
				sb.WriteString(fmt.Sprintf(", max %d words", slot.MaxWords))
			}
			if slot.Constraints != "" {
				sb.WriteString(fmt.Sprintf(", constraints: %s", slot.Constraints))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
		sb.WriteString("Return a JSON object where each key is a slot key and the value is the generated content.\n")
		sb.WriteString("For 'array' slots, the value should be a JSON array of objects.\n\n")
	} else {
		sb.WriteString("The template uses {{ render_blocks }} for content insertion.\n")
		sb.WriteString("Return a JSON object with key 'content' containing the HTML to inject.\n\n")
	}

	sb.WriteString("TASK:\n")
	sb.WriteString(instruction)

	return sb.String()
}
