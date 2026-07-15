package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/aigov"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// jsonMarshalImpl exists so tests can swap encoders if they need to. Currently
// just delegates to encoding/json.Marshal.
func jsonMarshalImpl(v interface{}) ([]byte, error) { return json.Marshal(v) }

// RegisterE2ETestRoutes mounts test-only endpoints used by the puppeteer
// harness. All routes are gated by SENTANYL_E2E_MODE=1; in production they
// reject every request with 403. Paths are relative — caller chooses prefix
// (e.g. /internal or /api/marketing/test).
func RegisterE2ETestRoutes(rg *gin.RouterGroup) {
	rg.POST("/simulate-purchase", handleSimulatePurchase)
	rg.POST("/simulate-refund", handleSimulateRefund)
	rg.POST("/ai-operation", handleE2EAIOperation)
	rg.POST("/ai-operation/:publicId/cancel", handleE2ECancelAIOperation)
}

type e2eAIOperationRequest struct {
	TenantID     string `json:"tenant_id" binding:"required"`
	Surface      string `json:"surface"`
	InputChars   int64  `json:"input_chars"`
	OutputTokens int64  `json:"output_tokens"`
	HoldMillis   int64  `json:"hold_millis"`
}

// handleE2EAIOperation drives the production admission/lease/cancellation
// kernel without spending provider tokens. It exists only in E2E mode.
func handleE2EAIOperation(c *gin.Context) {
	if os.Getenv("SENTANYL_E2E_MODE") != "1" {
		c.JSON(http.StatusForbidden, gin.H{"error": "e2e mode disabled"})
		return
	}
	var req e2eAIOperationRequest
	if err := c.ShouldBindJSON(&req); err != nil || !bson.IsObjectIdHex(req.TenantID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid tenant_id required"})
		return
	}
	if req.Surface == "" {
		req.Surface = "e2e.audit"
	}
	op, err := aigov.Begin(bson.ObjectIdHex(req.TenantID), req.Surface, aigov.Estimate{
		InputCharacters: req.InputChars, OutputTokens: req.OutputTokens,
	}, time.Now().UTC())
	if err != nil {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := aigov.Context(context.Background(), op)
	defer cancel()
	if req.HoldMillis > 0 {
		select {
		case <-time.After(time.Duration(req.HoldMillis) * time.Millisecond):
		case <-ctx.Done():
			_ = aigov.Fail(op, ctx.Err(), time.Now().UTC())
			c.JSON(http.StatusConflict, gin.H{"public_id": op.PublicID, "status": pkgmodels.AIOperationCanceled})
			return
		}
	}
	_ = aigov.Complete(op, aigov.Usage{}, time.Now().UTC())
	c.JSON(http.StatusOK, gin.H{"public_id": op.PublicID, "status": pkgmodels.AIOperationCompleted})
}

func handleE2ECancelAIOperation(c *gin.Context) {
	if os.Getenv("SENTANYL_E2E_MODE") != "1" {
		c.JSON(http.StatusForbidden, gin.H{"error": "e2e mode disabled"})
		return
	}
	var req struct {
		TenantID string `json:"tenant_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !bson.IsObjectIdHex(req.TenantID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid tenant_id required"})
		return
	}
	if err := aigov.RequestCancel(bson.ObjectIdHex(req.TenantID), c.Param("publicId"), time.Now().UTC()); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "active AI operation not found"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": pkgmodels.AIOperationCancelRequested})
}

// handleSimulateRefund drives the production refund handler with a synthetic
// stripe charge.refunded payload. This is the same code path the live Stripe
// webhook uses (processChargeRefunded), just without signature verification —
// gated by SENTANYL_E2E_MODE=1.
func handleSimulateRefund(c *gin.Context) {
	if os.Getenv("SENTANYL_E2E_MODE") != "1" {
		c.JSON(http.StatusForbidden, gin.H{"error": "e2e mode disabled"})
		return
	}
	var req struct {
		TenantID string `json:"tenant_id" binding:"required"`
		OfferID  string `json:"offer_id" binding:"required"`
		Email    string `json:"email" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !bson.IsObjectIdHex(req.TenantID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tenant_id"})
		return
	}
	tenantID := bson.ObjectIdHex(req.TenantID)

	charge := stripeCharge{
		ID: "ch_test_" + bson.NewObjectId().Hex(),
		Metadata: map[string]string{
			"offer_id":      req.OfferID,
			"contact_email": strings.ToLower(strings.TrimSpace(req.Email)),
		},
		Refunded: true,
	}
	raw, _ := jsonMarshal(charge)
	if err := processChargeRefunded(tenantID, raw); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "charge_id": charge.ID})
}

// jsonMarshal is a thin alias so the file doesn't need its own json import.
// (We already import encoding/json elsewhere in handlers/.)
func jsonMarshal(v interface{}) ([]byte, error) {
	return jsonMarshalImpl(v)
}

// simulatePurchaseRequest mirrors the data Stripe would deliver — without
// signing/verification — so the e2e harness can drive the full purchase →
// enroll → password-setup pipeline without a real Stripe round-trip. The
// underlying processCheckoutSessionCompleted is reused unchanged.
type simulatePurchaseRequest struct {
	TenantID       string `json:"tenant_id" binding:"required"`
	OfferID        string `json:"offer_id" binding:"required"`
	Email          string `json:"email" binding:"required"`
	Name           string `json:"name"`
	Domain         string `json:"domain"`
	SessionID      string `json:"session_id"`
	VideoSessionID string `json:"video_session_id"`
}

func handleSimulatePurchase(c *gin.Context) {
	if os.Getenv("SENTANYL_E2E_MODE") != "1" {
		c.JSON(http.StatusForbidden, gin.H{"error": "e2e mode disabled"})
		return
	}
	var req simulatePurchaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !bson.IsObjectIdHex(req.TenantID) || !bson.IsObjectIdHex(req.OfferID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tenant_id or offer_id"})
		return
	}
	tenantID := bson.ObjectIdHex(req.TenantID)

	var tenant pkgmodels.Tenant
	if err := db.GetCollection(pkgmodels.TenantCollection).FindId(tenantID).One(&tenant); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tenant not found"})
		return
	}

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = "cs_test_" + bson.NewObjectId().Hex()
	}
	domain := req.Domain
	if domain == "" {
		domain = "localhost"
	}

	metadata := map[string]string{
		"offer_id":  req.OfferID,
		"tenant_id": req.TenantID,
		"domain":    domain,
	}
	if req.VideoSessionID != "" {
		metadata["video_session_id"] = req.VideoSessionID
	}
	session := stripeCheckoutSession{
		ID:            sessionID,
		Mode:          "payment",
		CustomerEmail: strings.ToLower(strings.TrimSpace(req.Email)),
		Metadata:      metadata,
	}
	session.CustomerDetails.Email = session.CustomerEmail
	session.CustomerDetails.Name = req.Name
	raw, _ := json.Marshal(session)

	if err := processCheckoutSessionCompleted(tenantID, &tenant, raw); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Look up the resulting contact + token so the harness can drive the
	// /portal/set-password flow without scraping email.
	var contact pkgmodels.User
	if err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
		"tenant_id": tenantID,
		"email":     pkgmodels.EmailAddress(session.CustomerEmail),
	}).One(&contact); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "contact lookup failed"})
		return
	}

	// Only a hash is stored (ID-015), so the DB row can't be echoed back —
	// mint a fresh token the same way the checkout success page does.
	setupToken, _, err := setPasswordResetToken(contact.Id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "setup token mint failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"contact_id":           contact.Id.Hex(),
		"contact_public_id":    contact.PublicId,
		"email":                string(contact.Email),
		"password_reset_token": setupToken,
		"set_password_url":     buildPortalSetPasswordURL(domain, setupToken),
		"session_id":           sessionID,
	})
}
