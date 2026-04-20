package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/josephalai/sentanyl/marketing-service/internal/ai"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"gopkg.in/mgo.v2/bson"
)

// RegisterEmailAIRoutes wires AI email generation endpoints.
func RegisterEmailAIRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.POST("/email-ai/generate", handleAIGenerateEmail)
	tenantAPI.POST("/email-ai/edit", handleAIEditEmail)
}

// resolveContextPacks fetches context pack chunks for the given public IDs.
func resolveContextPacks(tenantID bson.ObjectId, packIDs []string) []string {
	var chunks []string
	for _, pid := range packIDs {
		var pack pkgmodels.ContextPack
		if err := db.GetCollection(pkgmodels.ContextPackCollection).Find(bson.M{
			"public_id": pid,
			"tenant_id": tenantID,
		}).One(&pack); err != nil {
			continue
		}
		for _, c := range pack.Chunks {
			chunks = append(chunks, c.Text)
		}
	}
	return chunks
}

// resolveBrandProfile builds a compact brand summary string for prompt injection.
func resolveBrandProfile(tenantID bson.ObjectId) string {
	var profile pkgmodels.BrandProfile
	if err := db.GetCollection(pkgmodels.BrandProfileCollection).Find(bson.M{"tenant_id": tenantID}).One(&profile); err != nil {
		return ""
	}
	var parts []string
	if profile.VoiceTone != "" {
		parts = append(parts, "Voice/Tone: "+profile.VoiceTone)
	}
	if profile.Positioning != "" {
		parts = append(parts, "Positioning: "+profile.Positioning)
	}
	if profile.AvatarDescription != "" {
		parts = append(parts, "Ideal Customer: "+profile.AvatarDescription)
	}
	if profile.CTAStyle != "" {
		parts = append(parts, "CTA Style: "+profile.CTAStyle)
	}
	if profile.FooterText != "" {
		parts = append(parts, "Default Footer: "+profile.FooterText)
	}
	if profile.LegalBlock != "" {
		parts = append(parts, "Legal/Disclaimer: "+profile.LegalBlock)
	}
	if len(parts) == 0 {
		return ""
	}
	result := ""
	for _, p := range parts {
		result += p + "\n"
	}
	return result
}

func handleAIGenerateEmail(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		Instruction  string   `json:"instruction" binding:"required"`
		ContextPacks []string `json:"context_pack_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	provider, err := ai.GetConfiguredProvider()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if provider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI provider not configured — set AI_PROVIDER, OPENAI_API_KEY or GEMINI_API_KEY"})
		return
	}

	chunks := resolveContextPacks(tenantID, req.ContextPacks)
	brandProfile := resolveBrandProfile(tenantID)

	result, err := provider.GenerateEmail(ai.EmailGenerationRequest{
		Instruction:   req.Instruction,
		ContextChunks: chunks,
		BrandProfile:  brandProfile,
	})
	if err != nil {
		log.Printf("[email-ai] generate error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI generation failed: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}

func handleAIEditEmail(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		Instruction    string   `json:"instruction" binding:"required"`
		CurrentSubject string   `json:"current_subject"`
		CurrentBody    string   `json:"current_body"`
		ContextPacks   []string `json:"context_pack_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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

	result, err := provider.EditEmail(ai.EmailEditRequest{
		Instruction:    req.Instruction,
		CurrentSubject: req.CurrentSubject,
		CurrentBody:    req.CurrentBody,
		ContextChunks:  chunks,
		BrandProfile:   brandProfile,
	})
	if err != nil {
		log.Printf("[email-ai] edit error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI edit failed: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}
