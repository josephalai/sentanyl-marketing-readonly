package routes

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/email"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// smtpProvider is initialised once from env vars at startup.
var smtpProvider email.EmailProvider

func init() {
	if os.Getenv("EMAIL_PROVIDER") != "" {
		smtpProvider = email.DefaultProvider()
		log.Printf("email: provider configured → %s", smtpProvider.Name())
	}
}

// RegisterEmailRoutes registers all email-related endpoints.
// These routes are unauthenticated and therefore only registered when
// PUBLIC_EMAIL_ROUTES=true (dev). Production callers use the tenant-scoped
// routes in email_tenant.go.
func RegisterEmailRoutes(rg *gin.RouterGroup) {
	if os.Getenv("PUBLIC_EMAIL_ROUTES") != "true" {
		log.Printf("email: public email routes disabled (PUBLIC_EMAIL_ROUTES != true)")
		return
	}
	rg.POST("/email", handleInsertEmail)
	rg.DELETE("/emails", handleClearUnsentEmails)
}

// sendEmailRequest is the shared payload for the public and tenant send routes.
type sendEmailRequest struct {
	From        string `json:"from"         binding:"required"`
	To          string `json:"to"           binding:"required"`
	SubjectLine string `json:"subject_line" binding:"required"`
	Html        string `json:"html"         binding:"required"`
	ReplyTo     string `json:"reply_to"`
}

func handleInsertEmail(c *gin.Context) {
	var req sendEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	insertAndSendEmail(c, req)
}

// nonRoutableTLDs are RFC-reserved TLDs that can never receive mail. Sends to
// them (e.g. e2e fixtures like user@e2e.local) are accepted and dropped so
// test flows stay green without generating hard bounces on the real MTA.
var nonRoutableTLDs = map[string]bool{
	"local": true, "localhost": true, "test": true, "invalid": true, "example": true,
}

func isNonRoutableRecipient(to string) bool {
	if i := strings.LastIndex(to, "."); i >= 0 && i < len(to)-1 {
		return nonRoutableTLDs[strings.ToLower(to[i+1:])]
	}
	return false
}

// insertAndSendEmail persists the message and dispatches it through the
// configured provider, then writes the API response.
func insertAndSendEmail(c *gin.Context, req sendEmailRequest) {
	msg := pkgmodels.NewInstantEmail()
	msg.From = req.From
	msg.To = req.To
	msg.SubjectLine = req.SubjectLine
	msg.Html = req.Html
	msg.ReplyTo = req.ReplyTo

	if err := db.GetCollection(pkgmodels.InstantEmailCollection).Insert(msg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to insert email"})
		return
	}

	if isNonRoutableRecipient(req.To) {
		log.Printf("email: suppressed non-routable recipient %s (reserved TLD)", req.To)
		c.JSON(http.StatusCreated, gin.H{
			"status": "OK", "id": msg.GetIdHex(), "public_id": msg.PublicId,
			"sent": false, "suppressed": true,
		})
		return
	}

	// Send immediately via the configured provider (MailHog in dev).
	sent := false
	if smtpProvider != nil {
		if err := smtpProvider.SendEmail(req.From, req.To, req.SubjectLine, req.Html, req.ReplyTo); err != nil {
			log.Printf("email: SMTP send failed: %v", err)
		} else {
			sent = true
			log.Printf("email: sent → %s subject=%q", req.To, req.SubjectLine)
		}
	}

	c.JSON(http.StatusCreated, gin.H{
		"status":    "OK",
		"id":        msg.GetIdHex(),
		"public_id": msg.PublicId,
		"sent":      sent,
	})
}

func handleClearUnsentEmails(c *gin.Context) {
	unsentQuery := bson.M{"$or": []interface{}{
		bson.M{"sent": nil},
		bson.M{"sent": bson.M{"$exists": false}},
	}}
	instant, err1 := db.GetCollection(pkgmodels.InstantEmailCollection).RemoveAll(unsentQuery)
	scheduled, err2 := db.GetCollection(pkgmodels.ScheduledEmailCollection).RemoveAll(unsentQuery)

	if err1 != nil || err2 != nil {
		errMsg := "unknown error"
		if err1 != nil {
			errMsg = err1.Error()
		} else if err2 != nil {
			errMsg = err2.Error()
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": errMsg})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":            "OK",
		"instant_removed":   instant.Removed,
		"scheduled_removed": scheduled.Removed,
	})
}
