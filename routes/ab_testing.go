package routes

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/emailer"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// RegisterABTestingRoutes mounts /api/ab/* — the A/B broadcast testing
// surface, rebuilt on the unified EmailSend tracking rows. The route shapes
// match the admin abService contract (frontend/src/services/crud.ts).
func RegisterABTestingRoutes(rg *gin.RouterGroup) {
	// Templates (reusable variant sets)
	rg.GET("/templates", handleABListTemplates)
	rg.POST("/templates", handleABCreateTemplate)
	rg.DELETE("/templates", handleABDeleteAllTemplates)
	rg.GET("/templates/:id", handleABGetTemplate)
	rg.PUT("/templates/:id/add", handleABTemplateAddVariant)
	rg.PUT("/templates/:id/template/:templateId", handleABTemplateModifyVariant)
	rg.DELETE("/templates/:id/template/:templateId", handleABTemplateDeleteVariant)

	// Tests
	rg.GET("/tests", handleABListTests)
	rg.POST("/tests", handleABCreateTest)
	rg.GET("/tests/:id", handleABGetTest)
	rg.PUT("/tests/:id", handleABUpdateTest)
	rg.DELETE("/tests/:id", handleABDeleteTest)

	// Operations
	rg.PUT("/operations/start/:id", handleABStartTest)
	rg.PUT("/operations/pause/:id", handleABSetStatus(pkgmodels.ABTestStatusPaused))
	rg.PUT("/operations/stop/:id", handleABSetStatus(pkgmodels.ABTestStatusCompleted))
	rg.GET("/operations/results/:id", handleABResults)
	rg.GET("/operations/result/all", handleABAllResults)
}

// abVariantCampaignKey is the EmailSend.CampaignPublicID rollup key for one
// variant of one test.
func abVariantCampaignKey(testPublicID, variantKey string) string {
	return testPublicID + ":" + variantKey
}

// ─── Templates ───────────────────────────────────────────────────────────────

func handleABListTemplates(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	var items []pkgmodels.ABTemplate
	_ = db.GetCollection(pkgmodels.ABTemplateCollection).Find(bson.M{
		"tenant_id": tenantID, "timestamps.deleted_at": nil,
	}).Sort("-_id").All(&items)
	if items == nil {
		items = []pkgmodels.ABTemplate{}
	}
	c.JSON(http.StatusOK, gin.H{"ab_templates": items})
}

func handleABCreateTemplate(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	t := pkgmodels.NewABTemplate(tenantID, req.Name)
	now := time.Now()
	t.SoftDeletes.CreatedAt = &now
	if err := db.GetCollection(pkgmodels.ABTemplateCollection).Insert(t); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "insert failed"})
		return
	}
	c.JSON(http.StatusCreated, t)
}

func handleABDeleteAllTemplates(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	_, _ = db.GetCollection(pkgmodels.ABTemplateCollection).UpdateAll(
		bson.M{"tenant_id": tenantID},
		bson.M{"$set": bson.M{"timestamps.deleted_at": time.Now()}},
	)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleABGetTemplate(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	var t pkgmodels.ABTemplate
	if err := db.GetCollection(pkgmodels.ABTemplateCollection).Find(bson.M{
		"public_id": c.Param("id"), "tenant_id": tenantID, "timestamps.deleted_at": nil,
	}).One(&t); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "template not found"})
		return
	}
	c.JSON(http.StatusOK, t)
}

func handleABTemplateAddVariant(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	var req struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	v := pkgmodels.ABTemplateVariant{PublicId: utils.GeneratePublicId(), Subject: req.Subject, Body: req.Body}
	if err := db.GetCollection(pkgmodels.ABTemplateCollection).Update(
		bson.M{"public_id": c.Param("id"), "tenant_id": tenantID},
		bson.M{"$push": bson.M{"templates": v}},
	); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "template not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "variant": v})
}

func handleABTemplateModifyVariant(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	var req struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if err := db.GetCollection(pkgmodels.ABTemplateCollection).Update(
		bson.M{"public_id": c.Param("id"), "tenant_id": tenantID, "templates.public_id": c.Param("templateId")},
		bson.M{"$set": bson.M{"templates.$.subject": req.Subject, "templates.$.body": req.Body}},
	); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "variant not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleABTemplateDeleteVariant(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if err := db.GetCollection(pkgmodels.ABTemplateCollection).Update(
		bson.M{"public_id": c.Param("id"), "tenant_id": tenantID},
		bson.M{"$pull": bson.M{"templates": bson.M{"public_id": c.Param("templateId")}}},
	); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "template not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ─── Tests ───────────────────────────────────────────────────────────────────

func handleABListTests(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	var items []pkgmodels.ABTest
	_ = db.GetCollection(pkgmodels.ABTestCollection).Find(bson.M{
		"tenant_id": tenantID, "timestamps.deleted_at": nil,
	}).Sort("-_id").All(&items)
	if items == nil {
		items = []pkgmodels.ABTest{}
	}
	c.JSON(http.StatusOK, gin.H{"ab_tests": items})
}

type abTestRequest struct {
	Name     string                       `json:"name"`
	Variants []pkgmodels.ABVariant        `json:"variants"`
	Audience *pkgmodels.CampaignAudience  `json:"audience"`
}

func handleABCreateTest(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	var req abTestRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	t := pkgmodels.NewABTest(tenantID, req.Name)
	t.Variants = req.Variants
	if len(t.Variants) == 0 {
		t.Variants = []pkgmodels.ABVariant{{Key: "A"}, {Key: "B"}}
	}
	if req.Audience != nil {
		t.Audience = *req.Audience
	}
	now := time.Now()
	t.SoftDeletes.CreatedAt = &now
	if err := db.GetCollection(pkgmodels.ABTestCollection).Insert(t); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "insert failed"})
		return
	}
	c.JSON(http.StatusCreated, t)
}

func handleABGetTest(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	var t pkgmodels.ABTest
	if err := db.GetCollection(pkgmodels.ABTestCollection).Find(bson.M{
		"public_id": c.Param("id"), "tenant_id": tenantID, "timestamps.deleted_at": nil,
	}).One(&t); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "test not found"})
		return
	}
	c.JSON(http.StatusOK, t)
}

func handleABUpdateTest(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	var req abTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	update := bson.M{}
	if strings.TrimSpace(req.Name) != "" {
		update["name"] = req.Name
	}
	if req.Variants != nil {
		update["variants"] = req.Variants
	}
	if req.Audience != nil {
		update["audience"] = *req.Audience
	}
	if len(update) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}
	if err := db.GetCollection(pkgmodels.ABTestCollection).Update(
		bson.M{"public_id": c.Param("id"), "tenant_id": tenantID, "status": pkgmodels.ABTestStatusDraft},
		bson.M{"$set": update},
	); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "test not found or already started"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleABDeleteTest(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	_ = db.GetCollection(pkgmodels.ABTestCollection).Update(
		bson.M{"public_id": c.Param("id"), "tenant_id": tenantID},
		bson.M{"$set": bson.M{"timestamps.deleted_at": time.Now()}},
	)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ─── Operations ─────────────────────────────────────────────────────────────

// handleABStartTest dispatches the experiment: the audience is resolved,
// split deterministically across variants (round-robin over the stable user
// list), each recipient gets that variant's email with the unified open
// pixel + click-tracked links, and a per-send EmailSend row keyed
// "<test>:<variant>". One-shot broadcasts complete immediately.
func handleABStartTest(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	var t pkgmodels.ABTest
	if err := db.GetCollection(pkgmodels.ABTestCollection).Find(bson.M{
		"public_id": c.Param("id"), "tenant_id": tenantID, "timestamps.deleted_at": nil,
	}).One(&t); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "test not found"})
		return
	}
	if t.Status == pkgmodels.ABTestStatusRunning || t.Status == pkgmodels.ABTestStatusCompleted {
		c.JSON(http.StatusConflict, gin.H{"error": "test already " + t.Status})
		return
	}
	if len(t.Variants) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a test needs at least two variants"})
		return
	}
	for _, v := range t.Variants {
		if strings.TrimSpace(v.Subject) == "" || strings.TrimSpace(v.Body) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "variant " + v.Key + " needs a subject and body"})
			return
		}
	}

	users, err := resolveCampaignAudience(tenantID, t.Audience)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(users) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "audience resolves to zero contacts"})
		return
	}

	count := 0
	for i, u := range users {
		v := t.Variants[i%len(t.Variants)]
		send := pkgmodels.NewEmailSend(tenantID, pkgmodels.EmailSendSourceCampaign, string(u.Email), v.Subject)
		send.ContactPublicID = u.PublicId
		send.CampaignPublicID = abVariantCampaignKey(t.PublicId, v.Key)
		if err := db.GetCollection(pkgmodels.EmailSendCollection).Insert(send); err != nil {
			continue
		}

		unsubURL := emailer.UnsubURL(publicBaseURL(), u.PublicId)
		html := rewriteABLinks(v.Body, send.PublicId)
		html = injectABPixel(html, send.PublicId)
		html = emailer.AppendUnsubFooter(html, unsubURL)

		msg := pkgmodels.NewInstantEmail()
		msg.From = "no-reply@sentanyl.local"
		msg.To = string(u.Email)
		msg.SubjectLine = v.Subject
		msg.Html = html
		if err := db.GetCollection(pkgmodels.InstantEmailCollection).Insert(msg); err != nil {
			continue
		}
		if smtpProvider != nil {
			if hs, ok := smtpProvider.(emailer.HeaderSender); ok {
				_ = hs.SendEmailWithHeaders(msg.From, msg.To, msg.SubjectLine, msg.Html, "", emailer.UnsubHeaders(unsubURL))
			} else {
				_ = smtpProvider.SendEmail(msg.From, msg.To, msg.SubjectLine, msg.Html, "")
			}
		}
		count++
	}

	now := time.Now()
	_ = db.GetCollection(pkgmodels.ABTestCollection).UpdateId(t.Id, bson.M{"$set": bson.M{
		"status":          pkgmodels.ABTestStatusCompleted, // one-shot broadcast experiment
		"recipient_count": count,
		"started_at":      now,
		"completed_at":    now,
	}})
	c.JSON(http.StatusOK, gin.H{"status": "ok", "recipient_count": count})
}

func handleABSetStatus(status string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := auth.GetTenantObjectID(c)
		if err := db.GetCollection(pkgmodels.ABTestCollection).Update(
			bson.M{"public_id": c.Param("id"), "tenant_id": tenantID},
			bson.M{"$set": bson.M{"status": status}},
		); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "test not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

// rewriteABLinks routes every href through the unified click redirect
// carrying the send id, so clicks stamp the EmailSend row.
func rewriteABLinks(body, sendPublicID string) string {
	return linkHrefRE.ReplaceAllStringFunc(body, func(match string) string {
		groups := linkHrefRE.FindStringSubmatch(match)
		if len(groups) < 4 {
			return match
		}
		pre, href, post := groups[1], groups[2], groups[3]
		hl := strings.ToLower(href)
		if strings.HasPrefix(hl, "mailto:") || strings.HasPrefix(hl, "tel:") || strings.HasPrefix(hl, "#") || strings.Contains(href, "/track/") {
			return match
		}
		tracked := fmt.Sprintf("%s/api/marketing/track/click?e=%s&u=%s", publicBaseURL(), sendPublicID, urlQueryEscape(href))
		return fmt.Sprintf(`<a %shref="%s"%s>`, pre, tracked, post)
	})
}

func injectABPixel(body, sendPublicID string) string {
	pixel := `<img src="` + publicBaseURL() + `/api/marketing/track/open?e=` + sendPublicID + `" width="1" height="1" style="display:none" alt=""/>`
	if i := strings.LastIndex(body, "</body>"); i >= 0 {
		return body[:i] + pixel + body[i:]
	}
	return body + pixel
}

// abVariantResults aggregates EmailSend rows for one test.
func abVariantResults(tenantID bson.ObjectId, t *pkgmodels.ABTest) gin.H {
	variants := make([]gin.H, 0, len(t.Variants))
	var winner string
	bestOpen, bestClick := -1.0, -1.0
	for _, v := range t.Variants {
		base := bson.M{"tenant_id": tenantID, "campaign_public_id": abVariantCampaignKey(t.PublicId, v.Key)}
		count := func(extra bson.M) int {
			q := bson.M{}
			for k, val := range base {
				q[k] = val
			}
			for k, val := range extra {
				q[k] = val
			}
			n, _ := db.GetCollection(pkgmodels.EmailSendCollection).Find(q).Count()
			return n
		}
		sent := count(nil)
		opened := count(bson.M{"opened_at": bson.M{"$ne": nil}})
		clicked := count(bson.M{"first_clicked_at": bson.M{"$ne": nil}})
		bounced := count(bson.M{"bounced_at": bson.M{"$ne": nil}})
		openRate, clickRate := 0.0, 0.0
		if sent > 0 {
			openRate = float64(opened) / float64(sent)
			clickRate = float64(clicked) / float64(sent)
		}
		if openRate > bestOpen || (openRate == bestOpen && clickRate > bestClick) {
			bestOpen, bestClick, winner = openRate, clickRate, v.Key
		}
		variants = append(variants, gin.H{
			"key": v.Key, "subject": v.Subject,
			"sent": sent, "opened": opened, "clicked": clicked, "bounced": bounced,
			"open_rate": openRate, "click_rate": clickRate,
		})
	}
	return gin.H{
		"test":     gin.H{"public_id": t.PublicId, "name": t.Name, "status": t.Status, "recipient_count": t.RecipientCount},
		"variants": variants,
		"winner":   winner,
	}
}

func handleABResults(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	var t pkgmodels.ABTest
	if err := db.GetCollection(pkgmodels.ABTestCollection).Find(bson.M{
		"public_id": c.Param("id"), "tenant_id": tenantID, "timestamps.deleted_at": nil,
	}).One(&t); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "test not found"})
		return
	}
	c.JSON(http.StatusOK, abVariantResults(tenantID, &t))
}

func handleABAllResults(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	var tests []pkgmodels.ABTest
	_ = db.GetCollection(pkgmodels.ABTestCollection).Find(bson.M{
		"tenant_id": tenantID, "timestamps.deleted_at": nil,
	}).Sort("-_id").All(&tests)
	out := make([]gin.H, 0, len(tests))
	for i := range tests {
		out = append(out, abVariantResults(tenantID, &tests[i]))
	}
	c.JSON(http.StatusOK, gin.H{"results": out})
}
