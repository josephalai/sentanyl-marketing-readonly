package routes

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterCampaignRoutes wires tenant-side campaign endpoints under the given
// router group (typically /api/tenant). Public click tracking is registered
// separately via RegisterCampaignTrackingRoutes since clicks come from email
// recipients, not authenticated tenants.
func RegisterCampaignRoutes(rg *gin.RouterGroup) {
	rg.GET("/campaigns", listCampaigns)
	rg.POST("/campaigns", createCampaign)
	rg.GET("/campaigns/:publicId", getCampaign)
	rg.PUT("/campaigns/:publicId", updateCampaign)
	rg.DELETE("/campaigns/:publicId", deleteCampaign)

	rg.POST("/campaigns/:publicId/preview", previewCampaign)
	rg.POST("/campaigns/:publicId/send", sendCampaign)
	rg.POST("/campaigns/:publicId/schedule", scheduleCampaign)

	rg.GET("/campaigns/:publicId/recipients", listCampaignRecipients)
}

func listCampaigns(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var out []pkgmodels.Campaign
	if err := db.GetCollection(pkgmodels.CampaignCollection).
		Find(bson.M{"tenant_id": tenantID}).
		Sort("-_id").
		All(&out); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"campaigns": out})
}

type campaignWritePayload struct {
	Name      string `json:"name"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	FromEmail string `json:"from_email"`
	FromName  string `json:"from_name"`
	ReplyTo   string `json:"reply_to"`
	Audience  *struct {
		MustHave    []string `json:"must_have"`
		MustNotHave []string `json:"must_not_have"`
	} `json:"audience"`
	ClickRules []pkgmodels.CampaignClickRule `json:"click_rules"`
}

func (p campaignWritePayload) applyTo(camp *pkgmodels.Campaign) {
	if p.Name != "" {
		camp.Name = p.Name
	}
	camp.Subject = p.Subject
	camp.Body = p.Body
	camp.FromEmail = p.FromEmail
	camp.FromName = p.FromName
	camp.ReplyTo = p.ReplyTo
	if p.Audience != nil {
		camp.Audience = pkgmodels.CampaignAudience{
			MustHave:    append([]string(nil), p.Audience.MustHave...),
			MustNotHave: append([]string(nil), p.Audience.MustNotHave...),
		}
	}
	camp.ClickRules = append([]pkgmodels.CampaignClickRule(nil), p.ClickRules...)
}

func createCampaign(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var p campaignWritePayload
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if p.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name required"})
		return
	}
	camp := pkgmodels.NewCampaign(tenantID, p.Name)
	p.applyTo(camp)

	if err := db.GetCollection(pkgmodels.CampaignCollection).Insert(camp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, camp)
}

func loadCampaign(c *gin.Context) (*pkgmodels.Campaign, bool) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return nil, false
	}
	publicID := c.Param("publicId")
	var camp pkgmodels.Campaign
	if err := db.GetCollection(pkgmodels.CampaignCollection).
		Find(bson.M{"tenant_id": tenantID, "public_id": publicID}).
		One(&camp); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "campaign not found"})
		return nil, false
	}
	return &camp, true
}

func getCampaign(c *gin.Context) {
	camp, ok := loadCampaign(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, camp)
}

func updateCampaign(c *gin.Context) {
	camp, ok := loadCampaign(c)
	if !ok {
		return
	}
	if camp.Status == pkgmodels.CampaignStatusSending || camp.Status == pkgmodels.CampaignStatusSent {
		c.JSON(http.StatusConflict, gin.H{"error": "cannot edit a campaign that is sending or sent"})
		return
	}
	var p campaignWritePayload
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	p.applyTo(camp)
	if err := db.GetCollection(pkgmodels.CampaignCollection).Update(
		bson.M{"_id": camp.Id},
		bson.M{"$set": bson.M{
			"name":        camp.Name,
			"subject":     camp.Subject,
			"body":        camp.Body,
			"from_email":  camp.FromEmail,
			"from_name":   camp.FromName,
			"reply_to":    camp.ReplyTo,
			"audience":    camp.Audience,
			"click_rules": camp.ClickRules,
		}},
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, camp)
}

func deleteCampaign(c *gin.Context) {
	camp, ok := loadCampaign(c)
	if !ok {
		return
	}
	if err := db.GetCollection(pkgmodels.CampaignCollection).Remove(bson.M{"_id": camp.Id}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// previewCampaign resolves the campaign's audience and returns a summary
// (count + first 3 sample emails) without sending.
func previewCampaign(c *gin.Context) {
	camp, ok := loadCampaign(c)
	if !ok {
		return
	}
	users, err := resolveCampaignAudience(camp.TenantID, camp.Audience)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	samples := make([]string, 0, 3)
	for i, u := range users {
		if i >= 3 {
			break
		}
		samples = append(samples, string(u.Email))
	}
	c.JSON(http.StatusOK, gin.H{
		"audience_size": len(users),
		"sample_emails": samples,
	})
}

func sendCampaign(c *gin.Context) {
	camp, ok := loadCampaign(c)
	if !ok {
		return
	}
	if camp.Status == pkgmodels.CampaignStatusSending || camp.Status == pkgmodels.CampaignStatusSent {
		c.JSON(http.StatusConflict, gin.H{"error": "campaign already sending or sent"})
		return
	}

	_ = db.GetCollection(pkgmodels.CampaignCollection).Update(
		bson.M{"_id": camp.Id},
		bson.M{"$set": bson.M{"status": pkgmodels.CampaignStatusSending}},
	)

	count, err := dispatchCampaign(camp, false, time.Time{})
	if err != nil {
		_ = db.GetCollection(pkgmodels.CampaignCollection).Update(
			bson.M{"_id": camp.Id},
			bson.M{"$set": bson.M{"status": pkgmodels.CampaignStatusFailed, "last_error": err.Error()}},
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	now := time.Now()
	_ = db.GetCollection(pkgmodels.CampaignCollection).Update(
		bson.M{"_id": camp.Id},
		bson.M{"$set": bson.M{
			"status":          pkgmodels.CampaignStatusSent,
			"sent_at":         now,
			"recipient_count": count,
		}},
	)
	c.JSON(http.StatusOK, gin.H{"status": "sent", "recipient_count": count})
}

func scheduleCampaign(c *gin.Context) {
	camp, ok := loadCampaign(c)
	if !ok {
		return
	}
	var req struct {
		ScheduledAt time.Time `json:"scheduled_at" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.ScheduledAt.Before(time.Now()) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scheduled_at must be in the future"})
		return
	}
	if camp.Status != pkgmodels.CampaignStatusDraft && camp.Status != pkgmodels.CampaignStatusScheduled {
		c.JSON(http.StatusConflict, gin.H{"error": "campaign cannot be scheduled in current status"})
		return
	}

	count, err := dispatchCampaign(camp, true, req.ScheduledAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_ = db.GetCollection(pkgmodels.CampaignCollection).Update(
		bson.M{"_id": camp.Id},
		bson.M{"$set": bson.M{
			"status":          pkgmodels.CampaignStatusScheduled,
			"scheduled_at":    req.ScheduledAt,
			"recipient_count": count,
		}},
	)
	c.JSON(http.StatusOK, gin.H{"status": "scheduled", "recipient_count": count, "scheduled_at": req.ScheduledAt})
}

func listCampaignRecipients(c *gin.Context) {
	camp, ok := loadCampaign(c)
	if !ok {
		return
	}
	var out []pkgmodels.CampaignRecipient
	if err := db.GetCollection(pkgmodels.CampaignRecipientCollection).
		Find(bson.M{"campaign_id": camp.Id}).
		Sort("-_id").
		Limit(500).
		All(&out); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"recipients": out})
}
