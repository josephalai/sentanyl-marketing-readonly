package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/ai"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterCampaignAIRoutes wires AI generate/edit for campaigns.
func RegisterCampaignAIRoutes(rg *gin.RouterGroup) {
	rg.POST("/campaigns/ai-generate", handleAIGenerateCampaign)
	rg.POST("/campaigns/:publicId/ai-edit-body", handleAIEditCampaignBody)
}

// listTenantBadges returns badge identifiers (public_id and name) so the LLM
// can select audience entries from the real catalog.
func listTenantBadges(tenantID bson.ObjectId) []string {
	var badges []pkgmodels.Badge
	if err := db.GetCollection(pkgmodels.BadgeCollection).
		Find(bson.M{"tenant_id": tenantID}).All(&badges); err != nil {
		return nil
	}
	out := make([]string, 0, len(badges))
	for _, b := range badges {
		if b.PublicId != "" {
			out = append(out, b.PublicId+" ("+b.Name+")")
		}
	}
	return out
}

func handleAIGenerateCampaign(c *gin.Context) {
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
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI provider not configured"})
		return
	}

	chunks := resolveContextPacks(tenantID, req.ContextPacks)
	brandProfile := resolveBrandProfile(tenantID)
	badges := listTenantBadges(tenantID)

	result, err := provider.GenerateEmail(ai.EmailGenerationRequest{
		Instruction:   req.Instruction,
		ContextChunks: chunks,
		BrandProfile:  brandProfile,
		BlockType:     ai.EmailBlockTypeCampaign,
		BadgeCatalog:  badges,
	})
	if err != nil {
		log.Printf("[campaign-ai] generate error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI generation failed: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

func handleAIEditCampaignBody(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	publicID := c.Param("publicId")
	var camp pkgmodels.Campaign
	if err := db.GetCollection(pkgmodels.CampaignCollection).
		Find(bson.M{"tenant_id": tenantID, "public_id": publicID}).One(&camp); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "campaign not found"})
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
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI provider not configured"})
		return
	}

	chunks := resolveContextPacks(tenantID, req.ContextPacks)
	brandProfile := resolveBrandProfile(tenantID)

	result, err := provider.EditEmail(ai.EmailEditRequest{
		Instruction:    req.Instruction,
		CurrentSubject: camp.Subject,
		CurrentBody:    camp.Body,
		ContextChunks:  chunks,
		BrandProfile:   brandProfile,
	})
	if err != nil {
		log.Printf("[campaign-ai] edit error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI edit failed: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}
