package routes

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	pkgauth "github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"

	"github.com/josephalai/sentanyl/pkg/utils"
)

// RegisterOutboundWebhookRoutes registers webhook CRUD endpoints. Mount on a
// tenant-authed group only — every handler scopes by the JWT tenant. Webhook
// signing secrets are integration secrets, so management requires owner-level
// secret permission (WH-001 / ID-001).
func RegisterOutboundWebhookRoutes(rg *gin.RouterGroup) {
	secrets := pkgauth.RequirePermission(pkgauth.PermSecretsManage)
	rg.GET("/outbound-webhooks", secrets, handleListOutboundWebhooks)
	rg.GET("/outbound-webhooks/:webhookId", secrets, handleGetOutboundWebhook)
	rg.POST("/outbound-webhooks", secrets, handleCreateOutboundWebhook)
	rg.PUT("/outbound-webhooks/:webhookId", secrets, handleUpdateOutboundWebhook)
	rg.POST("/outbound-webhooks/:webhookId/rotate-secret", secrets, handleRotateOutboundWebhookSecret)
	rg.DELETE("/outbound-webhooks/:webhookId", secrets, handleDeleteOutboundWebhook)
}

// generateWebhookSecret mints a new signing secret ("whsec_" + 40 base62).
func generateWebhookSecret() (string, error) {
	raw, err := pkgauth.GenerateAPIKey() // reuse the CSPRNG base62 generator
	if err != nil {
		return "", err
	}
	// GenerateAPIKey returns "snt_<40>"; re-label as a webhook secret.
	return "whsec_" + raw[len("snt_"):], nil
}

// storeWebhookSecret encrypts plaintext at rest and records display metadata.
func storeWebhookSecret(hook *pkgmodels.OutboundWebhook, plaintext string) error {
	enc, err := utils.EncryptSecret(plaintext)
	if err != nil {
		return err
	}
	hook.Secret = enc
	if len(plaintext) > 10 {
		hook.SecretPrefix = plaintext[:10]
	} else {
		hook.SecretPrefix = plaintext
	}
	now := time.Now()
	hook.SecretSetAt = &now
	return nil
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
	// The client cannot set the signing secret directly (Secret is json:"-").
	// Generate one server-side, store it encrypted, and return the plaintext
	// exactly once in this create response (WH-001).
	plaintext, err := generateWebhookSecret()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate secret"})
		return
	}
	if err := storeWebhookSecret(hook, plaintext); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to secure secret"})
		return
	}
	hook.SetCreated()
	if err := db.GetCollection(pkgmodels.OutboundWebhookCollection).Insert(hook); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create webhook"})
		return
	}
	// `secret` (plaintext) is present ONLY in this create response.
	c.JSON(http.StatusCreated, gin.H{"status": "OK", "outbound_webhook": hook, "secret": plaintext})
}

// handleRotateOutboundWebhookSecret issues a new signing secret and returns the
// plaintext once (WH-001).
func handleRotateOutboundWebhookSecret(c *gin.Context) {
	sId := pkgauth.GetTenantID(c)
	if sId == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var hook pkgmodels.OutboundWebhook
	if err := db.GetCollection(pkgmodels.OutboundWebhookCollection).Find(bson.M{
		"subscriber_id": sId,
		"public_id":     c.Param("webhookId"),
	}).One(&hook); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	plaintext, err := generateWebhookSecret()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate secret"})
		return
	}
	if err := storeWebhookSecret(&hook, plaintext); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to secure secret"})
		return
	}
	hook.SetUpdated()
	if err := db.GetCollection(pkgmodels.OutboundWebhookCollection).Update(
		bson.M{"subscriber_id": sId, "public_id": hook.PublicId},
		bson.M{"$set": bson.M{
			"secret":            hook.Secret,
			"secret_prefix":     hook.SecretPrefix,
			"secret_set_at":     hook.SecretSetAt,
			"timestamps.updated_at": hook.UpdatedAt,
		}},
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to rotate secret"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "OK", "secret": plaintext, "secret_prefix": hook.SecretPrefix})
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
