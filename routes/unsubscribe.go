package routes

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/emailer"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterUnsubscribeRoutes wires the public contact-level unsubscribe
// endpoint used by campaign/story/A-B emails (RFC 8058: GET shows a
// confirmation page for humans, POST is the mailbox-provider one-click).
// Newsletter unsubscribes keep their own subscription-scoped endpoint.
func RegisterUnsubscribeRoutes(rg *gin.RouterGroup) {
	rg.GET("/unsubscribe", handleContactUnsubscribe)
	rg.POST("/unsubscribe", handleContactUnsubscribe)
}

func handleContactUnsubscribe(c *gin.Context) {
	contactID := c.Query("u")
	token := c.Query("t")
	if contactID == "" || token == "" || !emailer.VerifyUnsubToken(contactID, token) {
		c.String(http.StatusBadRequest, "invalid unsubscribe link")
		return
	}
	now := time.Now().UTC()
	// Idempotent: repeat clicks re-stamp harmlessly. Scoped by public_id only
	// because the token already proves the link came from our own email.
	err := db.GetCollection(pkgmodels.UserCollection).Update(
		bson.M{"public_id": contactID},
		bson.M{"$set": bson.M{"subscribed": false, "unsubscribed_at": now}},
	)
	if err != nil {
		c.String(http.StatusNotFound, "contact not found")
		return
	}
	if c.Request.Method == http.MethodPost {
		c.Status(http.StatusOK) // one-click: body ignored by providers
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, `<!doctype html><html><body style="font-family:Helvetica,Arial,sans-serif;max-width:480px;margin:80px auto;text-align:center;color:#222">
<h2>You're unsubscribed</h2>
<p style="color:#666">You won't receive further marketing emails from this sender.</p>
</body></html>`)
}
