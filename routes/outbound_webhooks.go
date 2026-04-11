package routes

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	
	"github.com/josephalai/sentanyl/pkg/utils"
)

// RegisterOutboundWebhookRoutes registers webhook CRUD endpoints.
func RegisterOutboundWebhookRoutes(rg *gin.RouterGroup) {
	rg.GET("/outbound-webhooks", handleListOutboundWebhooks)
	rg.GET("/outbound-webhooks/:webhookId", handleGetOutboundWebhook)
	rg.POST("/outbound-webhooks", handleCreateOutboundWebhook)
	rg.PUT("/outbound-webhooks/:webhookId", handleUpdateOutboundWebhook)
	rg.DELETE("/outbound-webhooks/:webhookId", handleDeleteOutboundWebhook)
}

func handleListOutboundWebhooks(c *gin.Context) {
	sId := c.Query("subscriber_id")
	if sId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "subscriber_id is required"})
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
	webhookId := c.Param("webhookId")
	sId := c.Query("subscriber_id")
	if sId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "subscriber_id is required"})
		return
	}
	hook := pkgmodels.OutboundWebhook{}
	if err := db.GetCollection(pkgmodels.OutboundWebhookCollection).Find(bson.M{
		"subscriber_id": sId,
		"public_id":     webhookId,
	}).One(&hook); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "OK", "outbound_webhook": hook})
}

func handleCreateOutboundWebhook(c *gin.Context) {
	hook := pkgmodels.NewOutboundWebhook()
	if err := c.BindJSON(hook); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	hook.Id = bson.NewObjectId()
	hook.PublicId = utils.GeneratePublicId()
	hook.SetCreated()
	if err := db.GetCollection(pkgmodels.OutboundWebhookCollection).Insert(hook); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create webhook"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"status": "OK", "outbound_webhook": hook})
}

func handleUpdateOutboundWebhook(c *gin.Context) {
	webhookId := c.Param("webhookId")
	var req struct {
		SubscriberId string                 `json:"subscriber_id"`
		Webhook      pkgmodels.OutboundWebhook `json:"outbound_webhook"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	req.Webhook.SetUpdated()
	if err := db.GetCollection(pkgmodels.OutboundWebhookCollection).Update(
		bson.M{"subscriber_id": req.SubscriberId, "public_id": webhookId},
		bson.M{"$set": req.Webhook},
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update webhook"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "OK", "outbound_webhook": req.Webhook})
}

func handleDeleteOutboundWebhook(c *gin.Context) {
	webhookId := c.Param("webhookId")
	var req struct {
		SubscriberId string `json:"subscriber_id"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	hook := pkgmodels.OutboundWebhook{}
	if err := db.GetCollection(pkgmodels.OutboundWebhookCollection).Find(bson.M{
		"subscriber_id": req.SubscriberId,
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
