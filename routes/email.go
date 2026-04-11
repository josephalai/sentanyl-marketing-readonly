package routes

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	
)

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

	email := pkgmodels.NewInstantEmail()
	email.From = req.From
	email.To = req.To
	email.SubjectLine = req.SubjectLine
	email.Html = req.Html
	email.ReplyTo = req.ReplyTo

	if err := db.GetCollection(pkgmodels.InstantEmailCollection).Insert(email); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to insert email"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"status":    "OK",
		"id":        email.GetIdHex(),
		"public_id": email.PublicId,
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
