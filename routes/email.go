package routes

import (
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/email"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// smtpProvider is initialised once from env vars at startup.
var smtpProvider *email.SMTPProvider

func init() {
	if os.Getenv("EMAIL_PROVIDER") == "smtp" {
		host := os.Getenv("SMTP_HOST")
		if host == "" {
			host = "localhost"
		}
		port := 1025
		if p, err := strconv.Atoi(os.Getenv("SMTP_PORT")); err == nil {
			port = p
		}
		smtpProvider = email.NewSMTPProvider(host, port)
		log.Printf("email: SMTP provider configured → %s:%d", host, port)
	}
}

// RegisterEmailRoutes registers all email-related endpoints.
func RegisterEmailRoutes(rg *gin.RouterGroup) {
	rg.POST("/email", handleInsertEmail)
	rg.DELETE("/emails", handleClearUnsentEmails)
}

func handleInsertEmail(c *gin.Context) {
	var req struct {
		From        string `json:"from"         binding:"required"`
		To          string `json:"to"           binding:"required"`
		SubjectLine string `json:"subject_line" binding:"required"`
		Html        string `json:"html"         binding:"required"`
		ReplyTo     string `json:"reply_to"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

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

	// Send immediately via SMTP (MailHog in dev).
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
