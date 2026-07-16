package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterStylePresetRoutes wires the tenant-level Style Preset CRUD. Presets
// are a named, reusable brand system (visual tokens + voice) applied across the
// website builder, lead pages, and funnels.
func RegisterStylePresetRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.GET("/style-presets", handleListStylePresets)
	tenantAPI.POST("/style-presets", handleCreateStylePreset)
	tenantAPI.PUT("/style-presets/:presetId", handleUpdateStylePreset)
	tenantAPI.DELETE("/style-presets/:presetId", handleDeleteStylePreset)
}

func handleListStylePresets(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var presets []pkgmodels.StylePreset
	err := db.GetCollection(pkgmodels.StylePresetCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).All(&presets)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list style presets"})
		return
	}
	if presets == nil {
		presets = []pkgmodels.StylePreset{}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "presets": presets})
}

func handleCreateStylePreset(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req struct {
		Name        string                `json:"name"`
		GlobalStyle pkgmodels.GlobalStyle `json:"global_style"`
		BrandVoice  string                `json:"brand_voice"`
		DefaultTone string                `json:"default_tone"`
		IsDefault   bool                  `json:"is_default"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	preset := pkgmodels.NewStylePreset(tenantID, req.Name)
	preset.GlobalStyle = req.GlobalStyle
	preset.BrandVoice = req.BrandVoice
	preset.DefaultTone = req.DefaultTone
	preset.IsDefault = req.IsDefault
	if req.IsDefault {
		clearDefaultStylePreset(tenantID, "")
	}
	if err := db.GetCollection(pkgmodels.StylePresetCollection).Insert(preset); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create style preset"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"status": "ok", "preset": preset})
}

func handleUpdateStylePreset(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	presetID := c.Param("presetId")
	var req struct {
		Name        string                `json:"name"`
		GlobalStyle pkgmodels.GlobalStyle `json:"global_style"`
		BrandVoice  string                `json:"brand_voice"`
		DefaultTone string                `json:"default_tone"`
		IsDefault   bool                  `json:"is_default"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.IsDefault {
		clearDefaultStylePreset(tenantID, presetID)
	}
	err := db.GetCollection(pkgmodels.StylePresetCollection).Update(
		bson.M{"public_id": presetID, "tenant_id": tenantID, "timestamps.deleted_at": nil},
		bson.M{"$set": bson.M{
			"name":         req.Name,
			"global_style": req.GlobalStyle,
			"brand_voice":  req.BrandVoice,
			"default_tone": req.DefaultTone,
			"is_default":   req.IsDefault,
		}},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update style preset"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleDeleteStylePreset(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	presetID := c.Param("presetId")
	err := db.GetCollection(pkgmodels.StylePresetCollection).Update(
		bson.M{"public_id": presetID, "tenant_id": tenantID},
		bson.M{"$set": bson.M{"timestamps.deleted_at": time.Now().UTC()}},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete style preset"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// clearDefaultStylePreset unsets is_default on all of a tenant's presets except
// the one being promoted (exceptPublicID), so at most one preset is default.
func clearDefaultStylePreset(tenantID bson.ObjectId, exceptPublicID string) {
	q := bson.M{"tenant_id": tenantID, "is_default": true}
	if exceptPublicID != "" {
		q["public_id"] = bson.M{"$ne": exceptPublicID}
	}
	_, _ = db.GetCollection(pkgmodels.StylePresetCollection).UpdateAll(q, bson.M{"$set": bson.M{"is_default": false}})
}
