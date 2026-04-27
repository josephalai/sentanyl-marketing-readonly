package routes

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/ai"
	"github.com/josephalai/sentanyl/marketing-service/internal/site"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/render"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// RegisterNewsletterTenantRoutes mounts the tenant-side CRUD: newsletter
// config + tiers, post CRUD + publish, subscriber list, analytics. Caller has
// already wrapped the group in RequireTenantAuth.
func RegisterNewsletterTenantRoutes(rg *gin.RouterGroup) {
	rg.GET("/newsletters/:productId", handleGetNewsletter)
	rg.PUT("/newsletters/:productId", handleUpdateNewsletter)

	// Tier management. Free tier is auto-provisioned at product create time;
	// paid tiers link an existing Offer (created via the standard offer
	// builder) to a name + badge.
	rg.POST("/newsletters/:productId/tiers", handleCreateNewsletterTier)
	rg.PUT("/newsletters/:productId/tiers/:tierId", handleUpdateNewsletterTier)
	rg.DELETE("/newsletters/:productId/tiers/:tierId", handleDeleteNewsletterTier)

	// Posts.
	rg.GET("/newsletters/:productId/posts", handleListNewsletterPosts)
	rg.POST("/newsletters/:productId/posts", handleCreateNewsletterPost)
	rg.GET("/newsletters/:productId/posts/:postId", handleGetNewsletterPost)
	rg.PUT("/newsletters/:productId/posts/:postId", handleUpdateNewsletterPost)
	rg.DELETE("/newsletters/:productId/posts/:postId", handleDeleteNewsletterPost)
	rg.POST("/newsletters/:productId/posts/:postId/publish", handlePublishNewsletterPost)
	rg.POST("/newsletters/:productId/posts/:postId/unpublish", handleUnpublishNewsletterPost)

	// Subscribers + analytics.
	rg.GET("/newsletters/:productId/subscribers", handleListNewsletterSubscribers)
	rg.DELETE("/newsletters/:productId/subscribers/:subId", handleRemoveNewsletterSubscriber)
	rg.GET("/newsletters/:productId/analytics", handleNewsletterAnalytics)
}

// RegisterNewsletterCustomerRoutes mounts the customer-facing routes:
// list-my-newsletters, gated post fetch, upgrade-to-paid checkout-start.
// Caller has already wrapped the group in RequireCustomerAuth.
func RegisterNewsletterCustomerRoutes(rg *gin.RouterGroup) {
	rg.GET("/newsletters", handleCustomerListNewsletters)
	rg.GET("/newsletters/:productId", handleCustomerGetNewsletter)
	rg.GET("/newsletters/:productId/posts/:postId", handleCustomerGetNewsletterPost)
}

// RegisterNewsletterPublicRoutes mounts the unauth subscribe + double-opt-in
// + unsubscribe + public newsletter page renderer. The site renderer
// dispatches /newsletter and /newsletter/<slug> here when the resolved
// product type is newsletter.
func RegisterNewsletterPublicRoutes(rg *gin.RouterGroup) {
	rg.POST("/newsletters/subscribe", handlePublicNewsletterSubscribe)
	rg.GET("/newsletters/confirm", handlePublicNewsletterConfirm)
	rg.GET("/newsletters/unsubscribe", handlePublicNewsletterUnsubscribe)
	rg.GET("/newsletters/track/open", handleNewsletterTrackOpen)
	rg.GET("/newsletters/track/click", handleNewsletterTrackClick)
	rg.POST("/newsletters/webhook/:provider", handleNewsletterDeliveryWebhook)
}

// ---------- helpers ----------

func loadNewsletter(tenantID bson.ObjectId, productIDParam string) (*pkgmodels.Product, int, string) {
	if !bson.IsObjectIdHex(productIDParam) {
		return nil, http.StatusBadRequest, "invalid product id"
	}
	pid := bson.ObjectIdHex(productIDParam)
	var p pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"_id":                   pid,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&p); err != nil {
		return nil, http.StatusNotFound, "newsletter not found"
	}
	if p.ProductType != pkgmodels.ProductTypeNewsletter {
		return nil, http.StatusConflict, "product is not a newsletter"
	}
	if p.Newsletter == nil {
		// Self-heal: a product flipped to newsletter type without a config.
		p.Newsletter = &pkgmodels.NewsletterConfig{
			DoubleOptInEnabled:  true,
			DefaultPostAudience: pkgmodels.NewsletterAudienceAll,
		}
	}
	return &p, 0, ""
}

func loadNewsletterPost(tenantID, productID bson.ObjectId, idParam string) (*pkgmodels.NewsletterPost, int, string) {
	q := bson.M{
		"tenant_id":             tenantID,
		"product_id":            productID,
		"timestamps.deleted_at": nil,
	}
	if bson.IsObjectIdHex(idParam) {
		q["_id"] = bson.ObjectIdHex(idParam)
	} else {
		q["public_id"] = idParam
	}
	var p pkgmodels.NewsletterPost
	if err := db.GetCollection(pkgmodels.NewsletterPostCollection).Find(q).One(&p); err != nil {
		return nil, http.StatusNotFound, "post not found"
	}
	return &p, 0, ""
}

var slugCleanRE = regexp.MustCompile(`[^a-z0-9-]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "-")
	s = slugCleanRE.ReplaceAllString(s, "")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "post-" + utils.GeneratePublicId()[:8]
	}
	return s
}

func newOptInToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// Fall back to public-id; chance of collision is acceptable for an
		// opt-in confirmation that also has a 7-day expiry.
		return utils.GeneratePublicId()
	}
	return hex.EncodeToString(b)
}

// ---------- tenant: config ----------

func handleGetNewsletter(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	p, status, msg := loadNewsletter(tenantID, c.Param("productId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"product":    p,
		"newsletter": p.Newsletter,
	})
}

type updateNewsletterReq struct {
	HeroImageURL          *string  `json:"hero_image_url"`
	Tagline               *string  `json:"tagline"`
	Description           *string  `json:"description"`
	PublishCadence        *string  `json:"publish_cadence"`
	DoubleOptInEnabled    *bool    `json:"double_opt_in_enabled"`
	FromName              *string  `json:"from_name"`
	FromEmail             *string  `json:"from_email"`
	ReplyToEmail          *string  `json:"reply_to_email"`
	DefaultPostAudience   *string  `json:"default_post_audience"`
	DefaultAITTLSeconds   *int64   `json:"default_ai_ttl_seconds"`
	DefaultContextPackIDs []string `json:"default_context_pack_ids"`
}

func handleUpdateNewsletter(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	p, status, msg := loadNewsletter(tenantID, c.Param("productId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	var req updateNewsletterReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	cfg := p.Newsletter
	if req.HeroImageURL != nil {
		cfg.HeroImageURL = *req.HeroImageURL
	}
	if req.Tagline != nil {
		cfg.Tagline = *req.Tagline
	}
	if req.Description != nil {
		cfg.Description = *req.Description
	}
	if req.PublishCadence != nil {
		cfg.PublishCadence = *req.PublishCadence
	}
	if req.DoubleOptInEnabled != nil {
		cfg.DoubleOptInEnabled = *req.DoubleOptInEnabled
	}
	if req.FromName != nil {
		cfg.FromName = *req.FromName
	}
	if req.FromEmail != nil {
		cfg.FromEmail = *req.FromEmail
	}
	if req.ReplyToEmail != nil {
		cfg.ReplyToEmail = *req.ReplyToEmail
	}
	if req.DefaultPostAudience != nil {
		cfg.DefaultPostAudience = *req.DefaultPostAudience
	}
	if req.DefaultAITTLSeconds != nil {
		cfg.DefaultAITTLSeconds = *req.DefaultAITTLSeconds
	}
	if req.DefaultContextPackIDs != nil {
		// Resolve client-supplied public ids or hex to ObjectIds for the
		// stored config. Anything that doesn't resolve is silently dropped.
		ids := make([]bson.ObjectId, 0, len(req.DefaultContextPackIDs))
		for _, raw := range req.DefaultContextPackIDs {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			var pack pkgmodels.ContextPack
			if bson.IsObjectIdHex(raw) {
				if err := db.GetCollection(pkgmodels.ContextPackCollection).Find(bson.M{
					"_id":       bson.ObjectIdHex(raw),
					"tenant_id": tenantID,
				}).One(&pack); err == nil {
					ids = append(ids, pack.Id)
					continue
				}
			}
			if err := db.GetCollection(pkgmodels.ContextPackCollection).Find(bson.M{
				"public_id": raw,
				"tenant_id": tenantID,
			}).One(&pack); err == nil {
				ids = append(ids, pack.Id)
			}
		}
		cfg.DefaultContextPackIDs = ids
	}
	// Always ensure the free tier exists at index 0 — paid tiers add behind it.
	cfg.Tiers = ensureFreeTier(cfg.Tiers)

	if err := db.GetCollection(pkgmodels.ProductCollection).Update(
		bson.M{"_id": p.Id, "tenant_id": tenantID},
		bson.M{"$set": bson.M{"newsletter": cfg}},
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"newsletter": cfg})
}

func ensureFreeTier(tiers []*pkgmodels.NewsletterTier) []*pkgmodels.NewsletterTier {
	for _, t := range tiers {
		if t.IsFree {
			return tiers
		}
	}
	free := &pkgmodels.NewsletterTier{
		Id:     bson.NewObjectId(),
		Name:   "Free",
		IsFree: true,
		Order:  0,
	}
	return append([]*pkgmodels.NewsletterTier{free}, tiers...)
}

// ---------- tenant: tiers ----------

type tierUpsertReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	OfferID     string `json:"offer_id"`
	BadgeID     string `json:"badge_id"`
	Order       int    `json:"order"`
}

func handleCreateNewsletterTier(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	p, status, msg := loadNewsletter(tenantID, c.Param("productId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	var req tierUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name required"})
		return
	}
	if req.OfferID == "" || !bson.IsObjectIdHex(req.OfferID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid offer_id required for paid tier"})
		return
	}
	tier := &pkgmodels.NewsletterTier{
		Id:          bson.NewObjectId(),
		Name:        req.Name,
		Description: req.Description,
		OfferID:     bson.ObjectIdHex(req.OfferID),
		Order:       req.Order,
	}
	if req.BadgeID != "" && bson.IsObjectIdHex(req.BadgeID) {
		tier.BadgeID = bson.ObjectIdHex(req.BadgeID)
	}
	cfg := p.Newsletter
	cfg.Tiers = append(ensureFreeTier(cfg.Tiers), tier)
	if err := db.GetCollection(pkgmodels.ProductCollection).Update(
		bson.M{"_id": p.Id, "tenant_id": tenantID},
		bson.M{"$set": bson.M{"newsletter.tiers": cfg.Tiers}},
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save tier"})
		return
	}
	c.JSON(http.StatusCreated, tier)
}

func handleUpdateNewsletterTier(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	p, status, msg := loadNewsletter(tenantID, c.Param("productId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	tierID := c.Param("tierId")
	if !bson.IsObjectIdHex(tierID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tier id"})
		return
	}
	var req tierUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	tid := bson.ObjectIdHex(tierID)
	for _, t := range p.Newsletter.Tiers {
		if t.Id == tid {
			if req.Name != "" {
				t.Name = req.Name
			}
			t.Description = req.Description
			t.Order = req.Order
			if req.OfferID != "" && bson.IsObjectIdHex(req.OfferID) {
				t.OfferID = bson.ObjectIdHex(req.OfferID)
			}
			if req.BadgeID != "" && bson.IsObjectIdHex(req.BadgeID) {
				t.BadgeID = bson.ObjectIdHex(req.BadgeID)
			}
			break
		}
	}
	if err := db.GetCollection(pkgmodels.ProductCollection).Update(
		bson.M{"_id": p.Id, "tenant_id": tenantID},
		bson.M{"$set": bson.M{"newsletter.tiers": p.Newsletter.Tiers}},
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save tier"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tiers": p.Newsletter.Tiers})
}

func handleDeleteNewsletterTier(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	p, status, msg := loadNewsletter(tenantID, c.Param("productId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	tierID := c.Param("tierId")
	if !bson.IsObjectIdHex(tierID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tier id"})
		return
	}
	tid := bson.ObjectIdHex(tierID)
	out := make([]*pkgmodels.NewsletterTier, 0, len(p.Newsletter.Tiers))
	for _, t := range p.Newsletter.Tiers {
		if t.Id == tid && t.IsFree {
			c.JSON(http.StatusConflict, gin.H{"error": "cannot delete free tier"})
			return
		}
		if t.Id != tid {
			out = append(out, t)
		}
	}
	if err := db.GetCollection(pkgmodels.ProductCollection).Update(
		bson.M{"_id": p.Id, "tenant_id": tenantID},
		bson.M{"$set": bson.M{"newsletter.tiers": out}},
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tiers": out})
}

// ---------- tenant: posts ----------

func handleListNewsletterPosts(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	p, status, msg := loadNewsletter(tenantID, c.Param("productId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	var posts []pkgmodels.NewsletterPost
	_ = db.GetCollection(pkgmodels.NewsletterPostCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"product_id":            p.Id,
		"timestamps.deleted_at": nil,
	}).Sort("-published_at", "-_id").All(&posts)
	c.JSON(http.StatusOK, gin.H{"posts": posts})
}

type postUpsertReq struct {
	Title            string   `json:"title"`
	Subtitle         string   `json:"subtitle"`
	Slug             string   `json:"slug"`
	ThumbnailURL     string   `json:"thumbnail_url"`
	Tags             []string `json:"tags"`
	Authors          []string `json:"authors"`
	BodyDoc          bson.M   `json:"body_doc"`
	BodyMarkdown     string   `json:"body_markdown"`
	EmailSubject     string   `json:"email_subject"`
	EmailPreviewText string   `json:"email_preview_text"`
	Audience         string   `json:"audience"`
	HideFromWeb      bool     `json:"hide_from_web"`
	SEOTitle         string   `json:"seo_title"`
	SEODescription   string   `json:"seo_description"`
}

func handleCreateNewsletterPost(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	p, status, msg := loadNewsletter(tenantID, c.Param("productId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	var req postUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	post := pkgmodels.NewNewsletterPost(tenantID, p.Id)
	post.Title = req.Title
	post.Subtitle = req.Subtitle
	post.Slug = req.Slug
	if post.Slug == "" {
		post.Slug = slugify(req.Title)
	}
	post.ThumbnailURL = req.ThumbnailURL
	post.Tags = req.Tags
	post.Authors = req.Authors
	post.BodyDoc = req.BodyDoc
	post.BodyMarkdown = req.BodyMarkdown
	post.EmailSubject = req.EmailSubject
	post.EmailPreviewText = req.EmailPreviewText
	post.Audience = req.Audience
	if post.Audience == "" {
		post.Audience = p.Newsletter.DefaultPostAudience
	}
	post.HideFromWeb = req.HideFromWeb
	post.SEOTitle = req.SEOTitle
	post.SEODescription = req.SEODescription

	if err := db.GetCollection(pkgmodels.NewsletterPostCollection).Insert(post); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save post"})
		return
	}
	c.JSON(http.StatusCreated, post)
}

func handleGetNewsletterPost(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	p, status, msg := loadNewsletter(tenantID, c.Param("productId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	post, status, msg := loadNewsletterPost(tenantID, p.Id, c.Param("postId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	c.JSON(http.StatusOK, post)
}

func handleUpdateNewsletterPost(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	p, status, msg := loadNewsletter(tenantID, c.Param("productId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	post, status, msg := loadNewsletterPost(tenantID, p.Id, c.Param("postId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	var req postUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	set := bson.M{}
	if req.Title != "" {
		set["title"] = req.Title
	}
	if req.Subtitle != "" {
		set["subtitle"] = req.Subtitle
	}
	if req.Slug != "" {
		set["slug"] = slugify(req.Slug)
	}
	if req.ThumbnailURL != "" {
		set["thumbnail_url"] = req.ThumbnailURL
	}
	if req.Tags != nil {
		set["tags"] = req.Tags
	}
	if req.Authors != nil {
		set["authors"] = req.Authors
	}
	if req.BodyDoc != nil {
		set["body_doc"] = req.BodyDoc
	}
	if req.BodyMarkdown != "" {
		set["body_markdown"] = req.BodyMarkdown
	}
	if req.EmailSubject != "" {
		set["email_subject"] = req.EmailSubject
	}
	if req.EmailPreviewText != "" {
		set["email_preview_text"] = req.EmailPreviewText
	}
	if req.Audience != "" {
		set["audience"] = req.Audience
	}
	set["hide_from_web"] = req.HideFromWeb
	if req.SEOTitle != "" {
		set["seo_title"] = req.SEOTitle
	}
	if req.SEODescription != "" {
		set["seo_description"] = req.SEODescription
	}
	if err := db.GetCollection(pkgmodels.NewsletterPostCollection).Update(
		bson.M{"_id": post.Id, "tenant_id": tenantID},
		bson.M{"$set": set},
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "updated"})
}

func handleDeleteNewsletterPost(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	p, status, msg := loadNewsletter(tenantID, c.Param("productId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	post, status, msg := loadNewsletterPost(tenantID, p.Id, c.Param("postId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	if err := db.GetCollection(pkgmodels.NewsletterPostCollection).Update(
		bson.M{"_id": post.Id, "tenant_id": tenantID},
		bson.M{"$currentDate": bson.M{"timestamps.deleted_at": true}},
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// ---------- tenant: publish ----------

type publishReq struct {
	When string `json:"when"` // "now" or RFC3339
}

func handlePublishNewsletterPost(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	p, status, msg := loadNewsletter(tenantID, c.Param("productId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	post, status, msg := loadNewsletterPost(tenantID, p.Id, c.Param("postId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	var req publishReq
	_ = c.ShouldBindJSON(&req) // optional body — default = now

	scheduledAt := time.Now()
	scheduled := false
	if req.When != "" && req.When != "now" {
		t, err := time.Parse(time.RFC3339, req.When)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid when (RFC3339 expected)"})
			return
		}
		if t.After(time.Now().Add(30 * time.Second)) {
			scheduledAt = t
			scheduled = true
		}
	}

	sent, err := PublishPostNow(p, post, scheduled, scheduledAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":      post.Status,
		"scheduled":   scheduled,
		"emails_sent": sent,
	})
}

// PublishPostNow renders the post body, flips status, and runs the
// broadcast fan-out. Shared between the HTTP publish handler and the
// scheduler worker so absolute-mode scheduled posts auto-publish at their
// time without the tenant clicking anything. Drip-mode posts skip the
// flip — they go to status=published manually, then the dispatch worker
// fans out per-subscriber over time.
func PublishPostNow(p *pkgmodels.Product, post *pkgmodels.NewsletterPost, scheduled bool, scheduledAt time.Time) (int, error) {
	bodyDoc := map[string]any(post.BodyDoc)
	html := site.RenderPuckBodyOnly(bodyDoc)
	post.RenderedHTML = html

	if scheduled {
		post.Status = pkgmodels.NewsletterPostStatusScheduled
		post.ScheduledAt = &scheduledAt
		post.PublishedAt = nil
	} else {
		post.Status = pkgmodels.NewsletterPostStatusPublished
		now := time.Now()
		post.PublishedAt = &now
		post.ScheduledAt = nil
	}

	if err := db.GetCollection(pkgmodels.NewsletterPostCollection).Update(
		bson.M{"_id": post.Id, "tenant_id": post.TenantID},
		bson.M{"$set": bson.M{
			"status":        post.Status,
			"scheduled_at":  post.ScheduledAt,
			"published_at":  post.PublishedAt,
			"rendered_html": post.RenderedHTML,
		}},
	); err != nil {
		return 0, fmt.Errorf("failed to publish: %w", err)
	}

	sent, ferr := broadcastNewsletterPost(p, post, scheduled, scheduledAt)
	if ferr != nil {
		log.Printf("newsletter: broadcast had errors: %v", ferr)
	}
	return sent, nil
}

func handleUnpublishNewsletterPost(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	p, status, msg := loadNewsletter(tenantID, c.Param("productId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	post, status, msg := loadNewsletterPost(tenantID, p.Id, c.Param("postId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	if err := db.GetCollection(pkgmodels.NewsletterPostCollection).Update(
		bson.M{"_id": post.Id, "tenant_id": tenantID},
		bson.M{"$set": bson.M{"status": pkgmodels.NewsletterPostStatusDraft, "published_at": nil}},
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unpublish"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "draft"})
}

// ---------- tenant: subscribers + analytics ----------

func handleListNewsletterSubscribers(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	p, status, msg := loadNewsletter(tenantID, c.Param("productId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	q := bson.M{
		"tenant_id":             tenantID,
		"product_id":            p.Id,
		"timestamps.deleted_at": nil,
	}
	if st := c.Query("status"); st != "" {
		q["status"] = st
	}
	if tier := c.Query("tier"); tier != "" {
		q["tier_id"] = tier
	}
	var subs []pkgmodels.NewsletterSubscription
	_ = db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Find(q).Sort("-subscribed_at").Limit(500).All(&subs)
	c.JSON(http.StatusOK, gin.H{"subscribers": subs})
}

func handleRemoveNewsletterSubscriber(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	subID := c.Param("subId")
	if !bson.IsObjectIdHex(subID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Update(
		bson.M{"_id": bson.ObjectIdHex(subID), "tenant_id": tenantID},
		bson.M{"$set": bson.M{
			"status":          pkgmodels.NewsletterSubscriptionStatusUnsubscribed,
			"unsubscribed_at": time.Now(),
		}},
	); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "subscriber not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "removed"})
}

func handleNewsletterAnalytics(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	p, status, msg := loadNewsletter(tenantID, c.Param("productId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}

	// Roll up post stats. v1 ships overview only; opens/clicks plumbing comes
	// online once tracking pixel/click endpoints stamp newsletter_post_id.
	var posts []pkgmodels.NewsletterPost
	_ = db.GetCollection(pkgmodels.NewsletterPostCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"product_id":            p.Id,
		"status":                pkgmodels.NewsletterPostStatusPublished,
		"timestamps.deleted_at": nil,
	}).All(&posts)

	var totals pkgmodels.NewsletterPostStats
	for _, post := range posts {
		totals.Impressions += post.Stats.Impressions
		totals.EmailsSent += post.Stats.EmailsSent
		totals.Opens += post.Stats.Opens
		totals.Clicks += post.Stats.Clicks
		totals.Bounces += post.Stats.Bounces
		totals.Complaints += post.Stats.Complaints
		totals.Unsubscribes += post.Stats.Unsubscribes
	}

	activeSubs, _ := db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"product_id": p.Id,
		"status":     pkgmodels.NewsletterSubscriptionStatusActive,
	}).Count()
	pendingSubs, _ := db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"product_id": p.Id,
		"status":     pkgmodels.NewsletterSubscriptionStatusPending,
	}).Count()
	unsubs, _ := db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"product_id": p.Id,
		"status":     pkgmodels.NewsletterSubscriptionStatusUnsubscribed,
	}).Count()

	c.JSON(http.StatusOK, gin.H{
		"overview": gin.H{
			"posts_published":   len(posts),
			"active_subscribers": activeSubs,
			"pending_subscribers": pendingSubs,
			"unsubscribes":      unsubs,
		},
		"totals": totals,
		"posts":  posts,
	})
}

// ---------- customer ----------

func handleCustomerListNewsletters(c *gin.Context) {
	tenantID, contactID, ok := requireCustomer(c)
	if !ok {
		return
	}
	var subs []pkgmodels.NewsletterSubscription
	_ = db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"contact_id": contactID,
		"status":     pkgmodels.NewsletterSubscriptionStatusActive,
	}).All(&subs)
	productIDs := make([]bson.ObjectId, 0, len(subs))
	for _, s := range subs {
		productIDs = append(productIDs, s.ProductID)
	}
	var products []pkgmodels.Product
	if len(productIDs) > 0 {
		_ = db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
			"_id":       bson.M{"$in": productIDs},
			"tenant_id": tenantID,
		}).All(&products)
	}
	c.JSON(http.StatusOK, gin.H{
		"newsletters":   products,
		"subscriptions": subs,
	})
}

func handleCustomerGetNewsletter(c *gin.Context) {
	tenantID, contactID, ok := requireCustomer(c)
	if !ok {
		return
	}
	p, status, msg := loadNewsletter(tenantID, c.Param("productId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	var sub pkgmodels.NewsletterSubscription
	_ = db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"product_id": p.Id,
		"contact_id": contactID,
	}).One(&sub)

	var posts []pkgmodels.NewsletterPost
	_ = db.GetCollection(pkgmodels.NewsletterPostCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"product_id":            p.Id,
		"status":                pkgmodels.NewsletterPostStatusPublished,
		"hide_from_web":         false,
		"timestamps.deleted_at": nil,
	}).Sort("-published_at").All(&posts)

	c.JSON(http.StatusOK, gin.H{
		"product":      p,
		"subscription": sub,
		"posts":        posts,
	})
}

func handleCustomerGetNewsletterPost(c *gin.Context) {
	tenantID, contactID, ok := requireCustomer(c)
	if !ok {
		return
	}
	p, status, msg := loadNewsletter(tenantID, c.Param("productId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	post, status, msg := loadNewsletterPost(tenantID, p.Id, c.Param("postId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}

	viewer := buildViewerStateForContact(tenantID, p.Id, contactID)

	// Resolve {{ai}} handlebars before applying the gate split. Same
	// resolver, same cache the broadcast and public page use — this
	// reader sees the cached value for the current TTL window.
	resolvedHTML := post.RenderedHTML
	if resolver := ai.Resolver(); resolver != nil && p.Newsletter != nil {
		resolvedHTML = resolver.Resolve(post.RenderedHTML, render.ResolveOptions{
			TenantID:             tenantID,
			PostContextPackIDs:   post.ContextPackIDs,
			NewsletterDefaults:   p.Newsletter.DefaultContextPackIDs,
			NewsletterTTLSeconds: p.Newsletter.DefaultAITTLSeconds,
		})
	}
	splitR := render.SplitNewsletterPost(resolvedHTML, viewer)

	// Bump impressions opportunistically.
	_ = db.GetCollection(pkgmodels.NewsletterPostCollection).Update(
		bson.M{"_id": post.Id},
		bson.M{"$inc": bson.M{"stats.impressions": 1}},
	)

	c.JSON(http.StatusOK, gin.H{
		"post":         post,
		"visible_html": splitR.VisibleHTML,
		"gate":         splitR.GateCTA,
		"paywall_tier": splitR.PaywallTierID,
	})
}

func buildViewerStateForContact(tenantID, productID, contactID bson.ObjectId) render.ViewerState {
	var subs []pkgmodels.NewsletterSubscription
	_ = db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"product_id": productID,
		"contact_id": contactID,
		"status":     pkgmodels.NewsletterSubscriptionStatusActive,
	}).All(&subs)

	state := render.ViewerState{SubscribedTierIDs: map[string]bool{}}
	if len(subs) == 0 {
		state.Anonymous = true
		return state
	}
	for _, s := range subs {
		if s.TierID == pkgmodels.NewsletterFreeTierID || s.TierID == "" {
			state.SubscribedFree = true
		} else {
			state.SubscribedTierIDs[s.TierID] = true
		}
	}
	return state
}

// ---------- public: subscribe / confirm / unsubscribe ----------

type publicSubscribeReq struct {
	Domain    string `json:"domain"`
	ProductID string `json:"product_id"`
	Email     string `json:"email"`
	TierID    string `json:"tier_id"`
	Source    string `json:"source"`
}

func handlePublicNewsletterSubscribe(c *gin.Context) {
	var req publicSubscribeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email required"})
		return
	}

	domain := req.Domain
	if domain == "" {
		domain = c.GetHeader("X-Forwarded-Host")
	}
	if domain == "" {
		domain = c.Request.Host
	}
	s, err := site.FindSiteByDomain(domain)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "site not found"})
		return
	}
	tenantID := s.TenantID

	// Resolve product. Either explicit product_id or first newsletter on tenant.
	var product pkgmodels.Product
	if req.ProductID != "" && bson.IsObjectIdHex(req.ProductID) {
		_ = db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
			"_id":          bson.ObjectIdHex(req.ProductID),
			"tenant_id":    tenantID,
			"product_type": pkgmodels.ProductTypeNewsletter,
		}).One(&product)
	} else {
		_ = db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
			"tenant_id":             tenantID,
			"product_type":          pkgmodels.ProductTypeNewsletter,
			"timestamps.deleted_at": nil,
		}).One(&product)
	}
	if product.Id == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "newsletter not found"})
		return
	}
	if product.Newsletter == nil {
		product.Newsletter = &pkgmodels.NewsletterConfig{DoubleOptInEnabled: true}
	}

	contact, err := upsertNewsletterContact(tenantID, email)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record contact"})
		return
	}

	tierID := req.TierID
	if tierID == "" {
		tierID = pkgmodels.NewsletterFreeTierID
	}

	// Idempotent: re-use any existing pending/active row for this email.
	var existing pkgmodels.NewsletterSubscription
	err = db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"product_id": product.Id,
		"email":      email,
	}).One(&existing)

	requireOptIn := product.Newsletter.DoubleOptInEnabled
	now := time.Now()

	var sub *pkgmodels.NewsletterSubscription
	if err == nil {
		sub = &existing
		if sub.Status == pkgmodels.NewsletterSubscriptionStatusActive {
			c.JSON(http.StatusOK, gin.H{"status": "already_subscribed"})
			return
		}
		// Reset pending opt-in.
		sub.Status = pkgmodels.NewsletterSubscriptionStatusPending
		sub.OptInToken = newOptInToken()
		sub.UnsubscribeToken = newOptInToken()
		exp := now.Add(7 * 24 * time.Hour)
		sub.OptInExpiresAt = &exp
		sub.Source = req.Source
		_ = db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Update(
			bson.M{"_id": sub.Id},
			bson.M{"$set": bson.M{
				"status":            sub.Status,
				"opt_in_token":      sub.OptInToken,
				"opt_in_expires_at": sub.OptInExpiresAt,
				"unsubscribe_token": sub.UnsubscribeToken,
				"source":            sub.Source,
			}},
		)
	} else {
		sub = pkgmodels.NewNewsletterSubscription(tenantID, product.Id, contact.Id, email, tierID)
		sub.OptInToken = newOptInToken()
		sub.UnsubscribeToken = newOptInToken()
		exp := now.Add(7 * 24 * time.Hour)
		sub.OptInExpiresAt = &exp
		sub.Source = req.Source
		if !requireOptIn {
			sub.Status = pkgmodels.NewsletterSubscriptionStatusActive
			sub.ConfirmedAt = &now
		}
		if err := db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Insert(sub); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to subscribe"})
			return
		}
	}

	if requireOptIn {
		// Send the confirmation email through the existing pipeline.
		scheme := "https"
		if strings.Contains(domain, "lvh.me") || strings.Contains(domain, "localhost") {
			scheme = "http"
		}
		confirmURL := fmt.Sprintf("%s://%s/api/marketing/newsletters/confirm?token=%s", scheme, domain, sub.OptInToken)
		htmlBody := fmt.Sprintf(`<p>Confirm your subscription to <strong>%s</strong>.</p><p><a href="%s">Click here to confirm</a></p>`,
			htmlEscape(product.Name), confirmURL)
		fromEmail := product.Newsletter.FromEmail
		if fromEmail == "" {
			fromEmail = "no-reply@" + domain
		}
		insertEmailDoc(fromEmail, email, "Confirm your subscription", htmlBody)
		c.JSON(http.StatusAccepted, gin.H{"status": "pending_confirmation"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "subscribed"})
}

func handlePublicNewsletterConfirm(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.String(http.StatusBadRequest, "missing token")
		return
	}
	var sub pkgmodels.NewsletterSubscription
	if err := db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Find(bson.M{
		"opt_in_token": token,
	}).One(&sub); err != nil {
		c.String(http.StatusNotFound, "invalid token")
		return
	}
	if sub.OptInExpiresAt != nil && time.Now().After(*sub.OptInExpiresAt) {
		c.String(http.StatusGone, "confirmation link expired")
		return
	}
	now := time.Now()
	_ = db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Update(
		bson.M{"_id": sub.Id},
		bson.M{"$set": bson.M{
			"status":       pkgmodels.NewsletterSubscriptionStatusActive,
			"confirmed_at": now,
			"opt_in_token": "",
		}},
	)
	c.Writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.WriteString(`<!doctype html><html><body style="font-family:sans-serif;text-align:center;padding:40px">
<h1 data-testid="nl-confirmed">You're subscribed.</h1>
<p>Thanks for confirming. Your first issue will arrive in your inbox.</p>
<p><a href="/newsletter">Back to newsletter</a></p>
</body></html>`)
}

func handlePublicNewsletterUnsubscribe(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.String(http.StatusBadRequest, "missing token")
		return
	}
	now := time.Now()
	if err := db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Update(
		bson.M{"unsubscribe_token": token},
		bson.M{"$set": bson.M{
			"status":          pkgmodels.NewsletterSubscriptionStatusUnsubscribed,
			"unsubscribed_at": now,
		}},
	); err != nil {
		c.String(http.StatusNotFound, "invalid token")
		return
	}
	c.Writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = c.Writer.WriteString(`<!doctype html><html><body style="font-family:sans-serif;text-align:center;padding:40px">
<h1>You've been unsubscribed.</h1>
<p>You will no longer receive emails from this newsletter.</p>
</body></html>`)
}

// ---------- helpers private to this file ----------

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// insertEmailDoc writes an InstantEmail row and synchronously sends via the
// configured SMTP provider when present. Mirrors marketing-service/routes/email.go's
// handleInsertEmail so we get MailHog delivery in dev and real SMTP in prod.
func insertEmailDoc(from, to, subject, html string) {
	msg := pkgmodels.NewInstantEmail()
	msg.From = from
	msg.To = to
	msg.SubjectLine = subject
	msg.Html = html
	if err := db.GetCollection(pkgmodels.InstantEmailCollection).Insert(msg); err != nil {
		log.Printf("newsletter: failed to insert email: %v", err)
		return
	}
	if smtpProvider != nil {
		if err := smtpProvider.SendEmail(from, to, subject, html, ""); err != nil {
			log.Printf("newsletter: SMTP send failed: %v", err)
			return
		}
	}
}

// transparentGIF is the smallest possible 1x1 transparent GIF — written
// inline instead of read from disk so the binary is fully self-contained.
var transparentGIF = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00,
	0xFF, 0xFF, 0xFF, 0x21, 0xF9, 0x04, 0x01, 0x00, 0x00, 0x00, 0x00, 0x2C, 0x00, 0x00, 0x00, 0x00,
	0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x02, 0x44, 0x01, 0x00, 0x3B,
}

// handleNewsletterTrackOpen serves a 1x1 GIF and increments the post's open
// counter exactly once per (subscriber, post) pair. Re-firing across email
// client reloads is the dominant failure mode for naive open tracking; we
// dedupe via a $addToSet on a per-subscription opened_post_ids array and
// only $inc when the array did not already contain the post id (using a
// two-stage write so the counter is honest).
func handleNewsletterTrackOpen(c *gin.Context) {
	postPublicID := c.Query("p")
	subPublicID := c.Query("s")
	respondPixel := func() {
		c.Writer.Header().Set("Content-Type", "image/gif")
		c.Writer.Header().Set("Cache-Control", "no-store, max-age=0")
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.Write(transparentGIF)
	}
	if postPublicID == "" || subPublicID == "" {
		respondPixel()
		return
	}
	var post pkgmodels.NewsletterPost
	if err := db.GetCollection(pkgmodels.NewsletterPostCollection).Find(bson.M{
		"public_id": postPublicID,
	}).One(&post); err != nil {
		respondPixel()
		return
	}
	var sub pkgmodels.NewsletterSubscription
	if err := db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Find(bson.M{
		"public_id":  subPublicID,
		"product_id": post.ProductID,
	}).One(&sub); err != nil {
		respondPixel()
		return
	}
	// Two-step dedupe write: only increment opens when this is the first
	// time this subscriber has opened this post.
	change, err := db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Upsert(
		bson.M{"_id": sub.Id, "opened_post_ids": bson.M{"$ne": post.Id}},
		bson.M{"$addToSet": bson.M{"opened_post_ids": post.Id}},
	)
	if err == nil && change.Updated > 0 {
		_ = db.GetCollection(pkgmodels.NewsletterPostCollection).Update(
			bson.M{"_id": post.Id},
			bson.M{"$inc": bson.M{"stats.opens": 1}},
		)
	}
	respondPixel()
}

// handleNewsletterTrackClick increments the click counter, dedupes per
// (subscriber, post) the same way as opens, and 302s to the decoded target
// URL. The original URL is in the `u` query param (URL-escaped at email
// composition time).
func handleNewsletterTrackClick(c *gin.Context) {
	postPublicID := c.Query("p")
	subPublicID := c.Query("s")
	target := c.Query("u")
	if target == "" {
		c.String(http.StatusBadRequest, "missing target")
		return
	}
	if decoded, err := decodeQueryURL(target); err == nil {
		target = decoded
	}
	if postPublicID != "" && subPublicID != "" {
		var post pkgmodels.NewsletterPost
		if err := db.GetCollection(pkgmodels.NewsletterPostCollection).Find(bson.M{
			"public_id": postPublicID,
		}).One(&post); err == nil {
			var sub pkgmodels.NewsletterSubscription
			if err := db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Find(bson.M{
				"public_id":  subPublicID,
				"product_id": post.ProductID,
			}).One(&sub); err == nil {
				change, _ := db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Upsert(
					bson.M{"_id": sub.Id, "clicked_post_ids": bson.M{"$ne": post.Id}},
					bson.M{"$addToSet": bson.M{"clicked_post_ids": post.Id}},
				)
				if change != nil && change.Updated > 0 {
					_ = db.GetCollection(pkgmodels.NewsletterPostCollection).Update(
						bson.M{"_id": post.Id},
						bson.M{"$inc": bson.M{"stats.clicks": 1}},
					)
				}
			}
		}
	}
	c.Redirect(http.StatusFound, target)
}

// decodeQueryURL reverses the conservative escape applied by
// rewriteLinksForTracking. Order matters because % decodes back to literal %.
func decodeQueryURL(s string) (string, error) {
	r := strings.NewReplacer(
		"%26", "&",
		"%3D", "=",
		"%20", " ",
		"%3F", "?",
		"%23", "#",
		"%22", `"`,
		"%25", "%",
	)
	return r.Replace(s), nil
}

// handleNewsletterDeliveryWebhook ingests bounce, complaint, and
// unsubscribe events from any email-delivery provider. We accept a generic
// event shape and translate to NewsletterSubscription status updates. Real
// provider integrations (PowerMTA / Mailgun / Postmark / SES) plug in here
// by mapping their native event types onto these primitives in the URL
// path's :provider segment — for now everything routes through the same
// minimal envelope so we don't lose data while we wait on real wiring.
type deliveryWebhookEvent struct {
	Type      string `json:"type"`            // "bounce" | "complaint" | "unsubscribe" | "delivered"
	Email     string `json:"email"`
	MessageID string `json:"message_id,omitempty"`
	Timestamp int64  `json:"timestamp,omitempty"`
	// Provider-specific raw payload retained for audit. Free-form.
	Raw bson.M `json:"raw,omitempty"`
}

func handleNewsletterDeliveryWebhook(c *gin.Context) {
	provider := c.Param("provider")
	var ev deliveryWebhookEvent
	if err := c.ShouldBindJSON(&ev); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	email := strings.ToLower(strings.TrimSpace(ev.Email))
	if email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email required"})
		return
	}
	var newStatus string
	var statField string
	switch ev.Type {
	case "bounce":
		newStatus = pkgmodels.NewsletterSubscriptionStatusBounced
		statField = "stats.bounces"
	case "complaint":
		newStatus = pkgmodels.NewsletterSubscriptionStatusComplained
		statField = "stats.complaints"
	case "unsubscribe":
		newStatus = pkgmodels.NewsletterSubscriptionStatusUnsubscribed
		statField = "stats.unsubscribes"
	case "delivered":
		// Informational only — no status change.
		c.JSON(http.StatusOK, gin.H{"status": "ack"})
		return
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported event type"})
		return
	}
	now := time.Now()
	info, err := db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).UpdateAll(
		bson.M{"email": email, "status": pkgmodels.NewsletterSubscriptionStatusActive},
		bson.M{"$set": bson.M{
			"status":          newStatus,
			"unsubscribed_at": now,
		}},
	)
	matched := 0
	if info != nil {
		matched = info.Updated
	}
	// Bump per-post stats too, attributing to whichever post this subscriber
	// last received — the email_id query param would be the cleanest binding
	// but we don't carry it through MailHog. For now bump the most recent
	// published post for each affected subscription.
	if matched > 0 && statField != "" {
		var subs []pkgmodels.NewsletterSubscription
		_ = db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Find(bson.M{
			"email":  email,
			"status": newStatus,
		}).All(&subs)
		for _, s := range subs {
			var latest pkgmodels.NewsletterPost
			if err := db.GetCollection(pkgmodels.NewsletterPostCollection).Find(bson.M{
				"product_id": s.ProductID,
				"status":     pkgmodels.NewsletterPostStatusPublished,
			}).Sort("-published_at").One(&latest); err == nil {
				_ = db.GetCollection(pkgmodels.NewsletterPostCollection).Update(
					bson.M{"_id": latest.Id},
					bson.M{"$inc": bson.M{statField: 1}},
				)
			}
		}
	}
	_ = err
	c.JSON(http.StatusOK, gin.H{"status": "ok", "provider": provider, "matched": matched})
}

// upsertNewsletterContact finds or creates a User row keyed by (tenant, email).
// Mirrors handlers.upsertContact but lives here so newsletter routes don't
// have a cross-package dependency on the handlers package. Email is
// lowercased and trimmed before any read or write.
func upsertNewsletterContact(tenantID bson.ObjectId, email string) (*pkgmodels.User, error) {
	col := db.GetCollection(pkgmodels.UserCollection)
	clean := strings.ToLower(strings.TrimSpace(email))
	var existing pkgmodels.User
	if err := col.Find(bson.M{
		"email":     pkgmodels.EmailAddress(clean),
		"tenant_id": tenantID,
	}).One(&existing); err == nil {
		return &existing, nil
	}
	now := time.Now()
	u := pkgmodels.User{
		Id:       bson.NewObjectId(),
		PublicId: utils.GeneratePublicId(),
		TenantID: tenantID,
		Email:    pkgmodels.EmailAddress(clean),
	}
	u.SoftDeletes.CreatedAt = &now
	if err := col.Insert(u); err != nil {
		return nil, err
	}
	return &u, nil
}

// dummy reference to avoid unused import warnings in builds where only a
// subset of helpers are reached.
var _ = errors.New
