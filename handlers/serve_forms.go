package handlers

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// RegisterFormsRoutes wires tenant-scoped form management endpoints.
func RegisterFormsRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.GET("/forms", handleListForms)
	tenantAPI.POST("/forms", handleCreateForm)
	tenantAPI.GET("/forms/:formId", handleGetForm)
	tenantAPI.PUT("/forms/:formId", handleUpdateForm)
	tenantAPI.DELETE("/forms/:formId", handleDeleteForm)
}

func handleListForms(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var forms []pkgmodels.PageForm
	err := db.GetCollection(pkgmodels.PageFormCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).All(&forms)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list forms"})
		return
	}
	if forms == nil {
		forms = []pkgmodels.PageForm{}
	}
	c.JSON(http.StatusOK, forms)
}

func handleCreateForm(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req struct {
		Name     string                  `json:"name" binding:"required"`
		FormType string                  `json:"form_type"`
		Fields   []*pkgmodels.FormField  `json:"fields"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	now := time.Now()
	form := &pkgmodels.PageForm{
		Id:       bson.NewObjectId(),
		PublicId: utils.GeneratePublicId(),
		TenantID: tenantID,
		Name:     req.Name,
		FormType: req.FormType,
		Fields:   req.Fields,
	}
	form.CreatedAt = &now

	if err := db.GetCollection(pkgmodels.PageFormCollection).Insert(form); err != nil {
		log.Printf("[forms] insert error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create form"})
		return
	}
	c.JSON(http.StatusCreated, form)
}

func handleGetForm(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	formId := c.Param("formId")
	var form pkgmodels.PageForm
	if err := db.GetCollection(pkgmodels.PageFormCollection).Find(bson.M{
		"public_id": formId, "tenant_id": tenantID,
	}).One(&form); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "form not found"})
		return
	}
	c.JSON(http.StatusOK, form)
}

func handleUpdateForm(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	formId := c.Param("formId")
	var req struct {
		Name     string                  `json:"name"`
		FormType string                  `json:"form_type"`
		Fields   []*pkgmodels.FormField  `json:"fields"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	now := time.Now()
	update := bson.M{
		"timestamps.updated_at": now,
	}
	if req.Name != "" {
		update["name"] = req.Name
	}
	if req.FormType != "" {
		update["form_type"] = req.FormType
	}
	if req.Fields != nil {
		update["fields"] = req.Fields
	}

	if err := db.GetCollection(pkgmodels.PageFormCollection).Update(
		bson.M{"public_id": formId, "tenant_id": tenantID},
		bson.M{"$set": update},
	); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "form not found"})
		return
	}

	var updated pkgmodels.PageForm
	_ = db.GetCollection(pkgmodels.PageFormCollection).Find(bson.M{"public_id": formId}).One(&updated)
	c.JSON(http.StatusOK, updated)
}

func handleDeleteForm(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	formId := c.Param("formId")
	now := time.Now()
	if err := db.GetCollection(pkgmodels.PageFormCollection).Update(
		bson.M{"public_id": formId, "tenant_id": tenantID},
		bson.M{"$set": bson.M{"timestamps.deleted_at": now}},
	); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "form not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}
