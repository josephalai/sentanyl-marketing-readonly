package routes

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	pkgauth "github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"

	"github.com/josephalai/sentanyl/pkg/utils"
)

// RegisterOutboundWebhookRoutes registers webhook CRUD endpoints. Mount on a
// tenant-authed group only — every handler scopes by the JWT tenant.
func RegisterOutboundWebhookRoutes(rg *gin.RouterGroup) {
	rg.GET("/outbound-webhooks", handleListOutboundWebhooks)
	rg.GET("/outbound-webhooks/:webhookId", handleGetOutboundWebhook)
	rg.POST("/outbound-webhooks", handleCreateOutboundWebhook)
	rg.PUT("/outbound-webhooks/:webhookId", handleUpdateOutboundWebhook)
	rg.DELETE("/outbound-webhooks/:webhookId", handleDeleteOutboundWebhook)
}

func handleListOutboundWebhooks(c *gin.Context) {
	sId := pkgauth.GetTenantID(c)
	if sId == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var hooks []pkgmodels.OutboundWebhook
	if err := db.GetCollection(pkgmodels.OutboundWebhookCollection).Find(bson.M{
		"subscriber_id":         sId,
		"timestamps.deleted_at": bson.M{"$exists": false},
	}).All(&hooks); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "OK", "outbound_webhooks": hooks})
}

func handleGetOutboundWebhook(c *gin.Context) {
	sId := pkgauth.GetTenantID(c)
	if sId == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	hook := pkgmodels.OutboundWebhook{}
	if err := db.GetCollection(pkgmodels.OutboundWebhookCollection).Find(bson.M{
		"subscriber_id": sId,
		"public_id":     c.Param("webhookId"),
	}).One(&hook); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "OK", "outbound_webhook": hook})
}

func handleCreateOutboundWebhook(c *gin.Context) {
	sId := pkgauth.GetTenantID(c)
	if sId == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	hook := pkgmodels.NewOutboundWebhook()
	if err := c.BindJSON(hook); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	hook.Id = bson.NewObjectId()
	hook.PublicId = utils.GeneratePublicId()
	hook.SubscriberId = sId
	hook.SetCreated()
	if err := db.GetCollection(pkgmodels.OutboundWebhookCollection).Insert(hook); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create webhook"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"status": "OK", "outbound_webhook": hook})
}

func handleUpdateOutboundWebhook(c *gin.Context) {
	sId := pkgauth.GetTenantID(c)
	if sId == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var hook pkgmodels.OutboundWebhook
	if err := c.BindJSON(&hook); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	hook.Id = ""
	hook.PublicId = c.Param("webhookId")
	hook.SubscriberId = sId
	hook.SetUpdated()
	if err := db.GetCollection(pkgmodels.OutboundWebhookCollection).Update(
		bson.M{"subscriber_id": sId, "public_id": hook.PublicId},
		bson.M{"$set": hook},
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update webhook"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "OK", "outbound_webhook": hook})
}

func handleDeleteOutboundWebhook(c *gin.Context) {
	sId := pkgauth.GetTenantID(c)
	if sId == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	webhookId := c.Param("webhookId")
	hook := pkgmodels.OutboundWebhook{}
	if err := db.GetCollection(pkgmodels.OutboundWebhookCollection).Find(bson.M{
		"subscriber_id": sId,
		"public_id":     webhookId,
	}).One(&hook); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	hook.SetDeleted()
	hook.Active = false
	if err := db.GetCollection(pkgmodels.OutboundWebhookCollection).UpdateId(hook.Id, hook); err != nil {
		log.Printf("handleDeleteOutboundWebhook: error soft-deleting webhook %s: %v", webhookId, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete webhook"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "OK", "outbound_webhook": hook})
}
