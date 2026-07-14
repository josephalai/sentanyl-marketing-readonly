package routes

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/badges"
	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/linktoken"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterCampaignTrackingRoutes wires the public click tracker. Registered
// outside the tenant-auth group because clicks come from email recipients who
// have no JWT.
func RegisterCampaignTrackingRoutes(r *gin.Engine) {
	r.GET("/api/marketing/campaigns/track/click", handleCampaignClick)
}

// handleCampaignClick records a click against a campaign recipient, optionally
// awards a badge (when the link's click rule named one), and 302s to the
// destination. COM-002/006: the destination comes from a signed token (?t=) so
// this endpoint cannot be used as an open redirect; the legacy ?u= form is
// honored only for relative (same-origin) destinations. campaign+recipient via
// `c`/`r`; badge identifier via `b`.
func handleCampaignClick(c *gin.Context) {
	campPubID := c.Query("c")
	recPubID := c.Query("r")
	badgeIdent := c.Query("b")

	target := ""
	if tok := c.Query("t"); tok != "" {
		if _, _, dest, ok := linktoken.Verify(tok); ok {
			target = dest
		} else {
			c.Redirect(http.StatusFound, "/")
			return
		}
	} else {
		raw := c.Query("u")
		if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
			target = raw // relative same-origin only
		} else {
			target = "/" // refuse external raw destinations (open-redirect fix)
		}
	}

	if campPubID == "" || recPubID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing campaign/recipient"})
		return
	}

	var camp pkgmodels.Campaign
	if err := db.GetCollection(pkgmodels.CampaignCollection).
		Find(bson.M{"public_id": campPubID}).One(&camp); err != nil {
		c.Redirect(http.StatusFound, target)
		return
	}

	var rec pkgmodels.CampaignRecipient
	if err := db.GetCollection(pkgmodels.CampaignRecipientCollection).
		Find(bson.M{"public_id": recPubID, "campaign_id": camp.Id}).One(&rec); err != nil {
		c.Redirect(http.StatusFound, target)
		return
	}

	now := time.Now()
	_ = db.GetCollection(pkgmodels.CampaignRecipientCollection).Update(
		bson.M{"_id": rec.Id},
		bson.M{"$push": bson.M{"clicks": pkgmodels.CampaignClickEvent{URL: target, At: now}}},
	)

	// Stamp the unified per-email tracking row too.
	if sendID := c.Query("e"); sendID != "" {
		StampEmailSendClick(sendID, target)
	}

	if badgeIdent != "" {
		if err := awardCampaignBadge(camp.TenantID, rec.UserID, rec.Id, badgeIdent); err != nil {
			log.Printf("campaign-track: badge award failed: %v", err)
		}
	}

	c.Redirect(http.StatusFound, target)
}

// awardCampaignBadge looks up the badge by public_id or name within the tenant
// scope, appends its ObjectId to the user's badges array (idempotent via
// $addToSet), and records the award on the campaign recipient.
func awardCampaignBadge(tenantID, userID, recipientID bson.ObjectId, ident string) error {
	var badge pkgmodels.Badge
	if err := db.GetCollection(pkgmodels.BadgeCollection).Find(bson.M{
		"tenant_id": tenantID,
		"$or": []bson.M{
			{"public_id": ident},
			{"name": ident},
		},
	}).One(&badge); err != nil {
		return err
	}

	// ID-012: one grant per campaign recipient regardless of repeat clicks
	// (idempotent on the recipient ref), with durable provenance.
	if _, err := badges.Assign(tenantID, userID, badge.Id, "campaign_click", recipientID.Hex(), "system"); err != nil {
		return err
	}

	_ = db.GetCollection(pkgmodels.CampaignRecipientCollection).Update(
		bson.M{"_id": recipientID},
		bson.M{"$addToSet": bson.M{"badges_awarded": badge.PublicId}},
	)
	return nil
}
