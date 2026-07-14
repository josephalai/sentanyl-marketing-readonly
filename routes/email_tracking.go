package routes

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/linktoken"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterEmailTrackingRoutes wires the unified per-email open pixel and
// click redirect. Unauthenticated by design — hits come from email clients.
// Every send source (story engine, campaigns, newsletters) points its pixel
// here; clicks come here for campaign/newsletter sends (story clicks ride
// core-service's /api/track/click token path, which stamps the same rows).
func RegisterEmailTrackingRoutes(r *gin.Engine) {
	r.GET("/api/marketing/track/open", handleEmailTrackOpen)
	r.GET("/api/marketing/track/click", handleEmailTrackClick)
}

// emailTrackingGIF is a 1x1 transparent GIF (same bytes as the newsletter
// pixel).
var emailTrackingGIF = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00,
	0x00, 0x00, 0x00, 0x00, 0xFF, 0xFF, 0xFF, 0x21, 0xF9, 0x04, 0x01, 0x00,
	0x00, 0x00, 0x00, 0x2C, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00,
	0x00, 0x02, 0x02, 0x44, 0x01, 0x00, 0x3B,
}

// handleEmailTrackOpen is GET /api/marketing/track/open.
//
// COM-EM-006: the canonical form carries a signed token (?t=) binding tenant
// and send id, so opens cannot be stamped by guessing/enumerating send ids.
// The legacy unsigned ?e=<send public id> form is honored only for emails
// rendered before signing shipped. Always returns the pixel.
func handleEmailTrackOpen(c *gin.Context) {
	sendID := ""
	if tok := c.Query("t"); tok != "" {
		if _, sid, ok := linktoken.VerifyOpen(tok); ok {
			sendID = sid
		}
	} else {
		sendID = c.Query("e")
	}
	if sendID != "" {
		_ = db.GetCollection(pkgmodels.EmailSendCollection).Update(
			bson.M{"public_id": sendID, "opened_at": nil},
			bson.M{"$set": bson.M{"opened_at": time.Now()}},
		)
	}
	c.Header("Content-Type", "image/gif")
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
	_, _ = c.Writer.Write(emailTrackingGIF)
}

// ---------- Renderer-side signing (COM-EM-006) ----------

// Renderers rewrite links/pixels at template time, before per-recipient send
// rows exist, so they embed placeholders instead of tokens:
//
//	{{CLICK_TOKEN|<base64url of destination>}}
//	{{OPEN_TOKEN}}
//
// signEmailTrackingPlaceholders resolves them per recipient once the
// EmailSend row's public id is known, minting linktoken-signed values that
// bind (tenant, send, destination).
var (
	clickTokenPlaceholderRE = regexp.MustCompile(`\{\{CLICK_TOKEN\|([A-Za-z0-9_-]+)\}\}`)
	openTokenPlaceholder    = "{{OPEN_TOKEN}}"
)

// clickTokenPlaceholder encodes a destination URL into the placeholder a
// renderer embeds in the tracked link's t= parameter.
func clickTokenPlaceholder(destURL string) string {
	return "{{CLICK_TOKEN|" + base64.RawURLEncoding.EncodeToString([]byte(destURL)) + "}}"
}

func signEmailTrackingPlaceholders(html, tenantID, sendPublicID string) string {
	html = clickTokenPlaceholderRE.ReplaceAllStringFunc(html, func(m string) string {
		groups := clickTokenPlaceholderRE.FindStringSubmatch(m)
		if len(groups) != 2 {
			return ""
		}
		raw, err := base64.RawURLEncoding.DecodeString(groups[1])
		if err != nil {
			return ""
		}
		tok, err := linktoken.Sign(tenantID, sendPublicID, string(raw), 0)
		if err != nil {
			return ""
		}
		return tok
	})
	if strings.Contains(html, openTokenPlaceholder) {
		if tok, err := linktoken.SignOpen(tenantID, sendPublicID); err == nil {
			html = strings.ReplaceAll(html, openTokenPlaceholder, tok)
		} else {
			html = strings.ReplaceAll(html, openTokenPlaceholder, "")
		}
	}
	return html
}

// handleEmailTrackClick is GET /api/marketing/track/click.
//
// COM-002/006: the canonical form carries a signed opaque token
// (?t=<token>) that binds tenant, send id, destination, and expiry. Only the
// destination embedded in a valid token is honored, so Sentanyl domains can
// never be an open redirect. The legacy ?e=<send>&u=<url> form is accepted only
// for **relative** (same-origin) destinations during the transition; an
// external raw destination is refused and falls back to "/".
func handleEmailTrackClick(c *gin.Context) {
	if tok := c.Query("t"); tok != "" {
		if _, sendID, dest, ok := linktoken.Verify(tok); ok {
			if sendID != "" {
				StampEmailSendClick(sendID, dest)
			}
			c.Redirect(http.StatusFound, dest)
			return
		}
		// Present-but-invalid token: do not fall through to a raw redirect.
		c.Redirect(http.StatusFound, "/")
		return
	}

	// Legacy path: only relative destinations are allowed now.
	target := c.Query("u")
	if decoded, err := url.QueryUnescape(target); err == nil && decoded != "" {
		target = decoded
	}
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
		target = "/" // refuse absolute/external destinations (open-redirect fix)
	}
	if sendID := c.Query("e"); sendID != "" {
		StampEmailSendClick(sendID, target)
	}
	c.Redirect(http.StatusFound, target)
}

// StampEmailSendClick appends a click event to the unified send row, setting
// first_clicked_at on the first click.
func StampEmailSendClick(sendPublicID, target string) {
	now := time.Now()
	// first_clicked_at only once — conditional update, then unconditional push.
	_ = db.GetCollection(pkgmodels.EmailSendCollection).Update(
		bson.M{"public_id": sendPublicID, "first_clicked_at": nil},
		bson.M{"$set": bson.M{"first_clicked_at": now}},
	)
	_ = db.GetCollection(pkgmodels.EmailSendCollection).Update(
		bson.M{"public_id": sendPublicID},
		bson.M{"$push": bson.M{"click_events": pkgmodels.EmailClickEvent{URL: target, At: now}}},
	)
}

// MarkEmailSendBounced stamps a bounce on a send row resolved by SMTP
// message id, falling back to the most recent send to the address.
func MarkEmailSendBounced(tenantID bson.ObjectId, messageID, email string, at time.Time) {
	if messageID != "" {
		if err := db.GetCollection(pkgmodels.EmailSendCollection).Update(
			bson.M{"message_id": messageID, "bounced_at": nil},
			bson.M{"$set": bson.M{"bounced_at": at}},
		); err == nil {
			return
		}
	}
	if email == "" {
		return
	}
	var row pkgmodels.EmailSend
	q := bson.M{"recipient_email": email, "bounced_at": nil}
	if tenantID != "" {
		q["tenant_id"] = tenantID
	}
	if err := db.GetCollection(pkgmodels.EmailSendCollection).Find(q).Sort("-sent_at").One(&row); err != nil {
		return
	}
	_ = db.GetCollection(pkgmodels.EmailSendCollection).Update(
		bson.M{"_id": row.Id},
		bson.M{"$set": bson.M{"bounced_at": at}},
	)
}

// handleCampaignStats is GET /api/tenant/campaigns/:publicId/stats — the
// per-broadcast engagement rollup over the unified EmailSend rows.
func handleCampaignStats(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	campID := c.Param("publicId")
	var camp pkgmodels.Campaign
	if err := db.GetCollection(pkgmodels.CampaignCollection).Find(bson.M{"public_id": campID, "tenant_id": tenantID}).One(&camp); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "campaign not found"})
		return
	}

	base := bson.M{"campaign_public_id": campID, "tenant_id": tenantID}
	count := func(extra bson.M) int {
		q := bson.M{}
		for k, v := range base {
			q[k] = v
		}
		for k, v := range extra {
			q[k] = v
		}
		n, _ := db.GetCollection(pkgmodels.EmailSendCollection).Find(q).Count()
		return n
	}
	c.JSON(http.StatusOK, gin.H{
		"campaign": gin.H{"public_id": camp.PublicId, "name": camp.Name},
		"totals": gin.H{
			"sent":         count(nil),
			"opened":       count(bson.M{"opened_at": bson.M{"$ne": nil}}),
			"clicked":      count(bson.M{"first_clicked_at": bson.M{"$ne": nil}}),
			"bounced":      count(bson.M{"bounced_at": bson.M{"$ne": nil}}),
			"unsubscribed": count(bson.M{"unsubscribed_at": bson.M{"$ne": nil}}),
		},
	})
}
