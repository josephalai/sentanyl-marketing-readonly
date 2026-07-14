package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	pkgauth "github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/jobs"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// TriggerStoryStart calls core-service to start executing a story for a user.
// Runs in a goroutine — fire and forget. Exported so other packages
// (e.g. marketing-service/internal/forms) can reuse the same dispatch path
// rather than duplicating the subscriber-id lookup logic.
// Story dispatch (ACQ-003): starting a story is a durable job, not a
// fire-and-forget goroutine — the enqueue is acknowledged only after the
// command is persisted, and the job kernel retries/dead-letters the
// core-service call. Lookups are tenant-scoped by the caller.
const storyStartJobType = "story.start"

// RegisterStoryStartJob binds the story.start handler. Call at startup after
// the jobs worker is configured.
func RegisterStoryStartJob() {
	jobs.Register(storyStartJobType, func(ctx context.Context, job *jobs.Job) error {
		storyName, _ := job.Payload["story_name"].(string)
		subscriberID, _ := job.Payload["subscriber_id"].(string)
		userPublicID, _ := job.Payload["user_public_id"].(string)
		if storyName == "" || subscriberID == "" || userPublicID == "" {
			return fmt.Errorf("story.start: incomplete payload %v", job.Payload)
		}
		coreURL := os.Getenv("CORE_SERVICE_URL")
		if coreURL == "" {
			coreURL = "http://core-service:8081"
		}
		payload, _ := json.Marshal(map[string]string{
			"story_name":     storyName,
			"subscriber_id":  subscriberID,
			"user_public_id": userPublicID,
		})
		req, err := http.NewRequest(http.MethodPost, coreURL+"/internal/story/start", bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		pkgauth.AttachServiceAuth(req, "marketing") // API-001 signed service identity
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("story.start call: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("story.start returned %d", resp.StatusCode)
		}
		log.Printf("story.start: story=%q user=%s tenant=%s → HTTP %d", storyName, userPublicID, subscriberID, resp.StatusCode)
		return nil
	})
}

// EnqueueStoryStart persists a durable story-start command for a
// tenant-resolved contact. Bursts within the same minute collapse via the
// idempotency key (StartStoryForUser also supersedes duplicate sessions).
func EnqueueStoryStart(subscriberID, storyName, userPublicID string) error {
	key := fmt.Sprintf("%s:%s:%s:%d", subscriberID, storyName, userPublicID, time.Now().Unix()/60)
	env := jobs.Envelope{Actor: "funnel", Subject: "story:" + storyName, Version: 1}
	if bson.IsObjectIdHex(subscriberID) {
		env.TenantID = bson.ObjectIdHex(subscriberID)
	}
	return jobs.Enqueue(jobs.NewJob(storyStartJobType, key, env, bson.M{
		"story_name":     storyName,
		"subscriber_id":  subscriberID,
		"user_public_id": userPublicID,
	}))
}

// RegisterLegacyTenantFunnelRoutes wires the funnels list under /api/tenant/*
// so that frontend pages built against the old monolith paths keep working.
// Caddy routes /api/tenant/funnels* to this service.
//
// /api/tenant/purchases was superseded by handlers.RegisterPurchasesRoutes
// (tenant-scoped via JWT, hydrated DTOs); the legacy query-param product/
// purchase handlers that used to live in this file have been removed.
func RegisterLegacyTenantFunnelRoutes(rg *gin.RouterGroup) {
	rg.GET("/funnels", handleGetFunnels)
	rg.GET("/funnels/:funnelId", handleGetFunnel)
	rg.POST("/funnels", handleCreateFunnel)
	rg.PUT("/funnels/:funnelId", handleUpdateFunnel)
	rg.DELETE("/funnels/:funnelId", handleDeleteFunnel)
}

// RegisterLegacyFunnelTemplateRoutes wires funnel-template CRUD under /api/funnel/*
// to match the legacy path /api/funnel/template used by FunnelTemplatesPage.
func RegisterLegacyFunnelTemplateRoutes(rg *gin.RouterGroup) {
	rg.GET("/template", handleGetFunnelTemplates)
	rg.GET("/template/default", handleGetDefaultFunnelTemplate)
	rg.GET("/template/:templateId", handleGetFunnelTemplate)
	rg.POST("/template", handleCreateFunnelTemplate)
	rg.PUT("/template/:templateId", handleUpdateFunnelTemplate)
	rg.DELETE("/template/:templateId", handleDeleteFunnelTemplate)
}

// handleGetDefaultFunnelTemplate returns the tenant's default template for
// a given kind (e.g. squeeze_page, lead_magnet). Falls back to the most-
// recently created template of that kind when no explicit default is set,
// and returns 404 only when the tenant has no template of that kind at all.
func handleGetDefaultFunnelTemplate(c *gin.Context) {
	tenantID := pkgauth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	kind := c.Query("kind")
	if kind == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind query parameter required"})
		return
	}
	var tmpl pkgmodels.FunnelTemplate
	// Prefer the tenant-marked default.
	err := db.GetCollection(pkgmodels.FunnelTemplateCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"template_kind":         kind,
		"default_for_tenant":    true,
		"timestamps.deleted_at": nil,
	}).One(&tmpl)
	if err != nil {
		// Fallback: latest of that kind.
		err = db.GetCollection(pkgmodels.FunnelTemplateCollection).Find(bson.M{
			"tenant_id":             tenantID,
			"template_kind":         kind,
			"timestamps.deleted_at": nil,
		}).Sort("-timestamps.created_at").One(&tmpl)
	}
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no template for kind " + kind})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "template": tmpl})
}

// RegisterFunnelRoutes registers the genuinely public funnel endpoints
// (page serving + event ingestion). The funnel/product/purchase/template
// CRUD that used to live here trusted a subscriber_id query param with no
// auth; the surviving handlers are JWT-scoped and mounted only on the
// tenant-authed legacy groups (RegisterLegacyTenantFunnelRoutes /
// RegisterLegacyFunnelTemplateRoutes). The product/purchase variants were
// superseded by RegisterEcommerceRoutes + handlers.RegisterPurchasesRoutes
// and have been removed.
func RegisterFunnelRoutes(rg *gin.RouterGroup) {
	rg.GET("/funnel/page", handleGetPageByDomainPath)
	rg.POST("/funnel/event", handleFunnelEvent)
}

// ---------- Funnel CRUD ----------

// jwtTenantOr returns the tenant_id/subscriber_id $or clause for the JWT
// tenant (same dual-key pattern as handleUpdateFunnel), or nil when the
// request carries no tenant identity.
func jwtTenantOr(c *gin.Context) []bson.M {
	if pkgauth.GetTenantID(c) == "" {
		return nil
	}
	return []bson.M{
		{"tenant_id": pkgauth.GetTenantObjectID(c)},
		{"subscriber_id": pkgauth.GetTenantID(c)},
	}
}

func handleGetFunnels(c *gin.Context) {
	scope := jwtTenantOr(c)
	if scope == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var funnels []pkgmodels.Funnel
	db.GetCollection(pkgmodels.FunnelCollection).Find(bson.M{
		"$or":                   scope,
		"timestamps.deleted_at": nil,
	}).All(&funnels)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "funnels": funnels})
}

func handleGetFunnel(c *gin.Context) {
	scope := jwtTenantOr(c)
	if scope == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var funnel pkgmodels.Funnel
	err := db.GetCollection(pkgmodels.FunnelCollection).Find(bson.M{"public_id": c.Param("funnelId"), "$or": scope}).One(&funnel)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "funnel not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "funnel": funnel})
}

// normalizeFunnelTree gives every embedded route/stage real ids, ownership,
// and 1-based order. Without this, docs with empty nested _id fields fail
// bson marshalling ("ObjectIDs must be exactly 12 bytes long").
func normalizeFunnelTree(funnel *pkgmodels.Funnel) {
	for ri, route := range funnel.Routes {
		if route == nil {
			continue
		}
		if route.Id == "" {
			route.Id = bson.NewObjectId()
		}
		if route.PublicId == "" {
			route.PublicId = utils.GeneratePublicId()
		}
		route.FunnelId = funnel.Id
		route.TenantID = funnel.TenantID
		route.SubscriberId = funnel.SubscriberId
		route.Order = ri + 1
		for si, stage := range route.Stages {
			if stage == nil {
				continue
			}
			if stage.Id == "" {
				stage.Id = bson.NewObjectId()
			}
			if stage.PublicId == "" {
				stage.PublicId = utils.GeneratePublicId()
			}
			stage.RouteId = route.Id
			stage.TenantID = funnel.TenantID
			stage.SubscriberId = funnel.SubscriberId
			stage.Order = si + 1
		}
	}
}

func handleCreateFunnel(c *gin.Context) {
	var funnel pkgmodels.Funnel
	if err := c.ShouldBindJSON(&funnel); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid funnel data"})
		return
	}
	now := time.Now()
	funnel.Id = bson.NewObjectId()
	funnel.PublicId = utils.GeneratePublicId()
	// Ownership always comes from the JWT, never the request body.
	funnel.TenantID = pkgauth.GetTenantObjectID(c)
	funnel.SubscriberId = pkgauth.GetTenantID(c)
	if funnel.SubscriberId == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	normalizeFunnelTree(&funnel)
	funnel.SoftDeletes.CreatedAt = &now
	if err := db.GetCollection(pkgmodels.FunnelCollection).Insert(funnel); err != nil {
		log.Printf("create funnel: insert failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create funnel"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "funnel": funnel})
}

// handleUpdateFunnel persists the funnel builder's structure edits: name and
// the routes[]/stages[] tree (rename, reorder, add, remove). Stage pages and
// triggers ride through untouched — the UI round-trips them from GET.
// Scoped to the caller's tenant (tenant_id or the legacy subscriber_id key).
func handleUpdateFunnel(c *gin.Context) {
	var req struct {
		Name   string                   `json:"name"`
		Routes []*pkgmodels.FunnelRoute `json:"routes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid funnel data"})
		return
	}

	scope := bson.M{
		"public_id":             c.Param("funnelId"),
		"timestamps.deleted_at": nil,
		"$or": []bson.M{
			{"tenant_id": pkgauth.GetTenantObjectID(c)},
			{"subscriber_id": pkgauth.GetTenantID(c)},
		},
	}
	var funnel pkgmodels.Funnel
	if err := db.GetCollection(pkgmodels.FunnelCollection).Find(scope).One(&funnel); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "funnel not found"})
		return
	}

	set := bson.M{"timestamps.updated_at": time.Now()}
	if req.Name != "" {
		set["name"] = req.Name
	}
	if req.Routes != nil {
		tree := funnel
		tree.Routes = req.Routes
		normalizeFunnelTree(&tree)
		set["routes"] = tree.Routes
	}
	if err := db.GetCollection(pkgmodels.FunnelCollection).UpdateId(funnel.Id, bson.M{"$set": set}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update funnel"})
		return
	}
	if err := db.GetCollection(pkgmodels.FunnelCollection).FindId(funnel.Id).One(&funnel); err == nil {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "funnel": funnel})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleDeleteFunnel(c *gin.Context) {
	scope := jwtTenantOr(c)
	if scope == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	now := time.Now()
	db.GetCollection(pkgmodels.FunnelCollection).Update(
		bson.M{"public_id": c.Param("funnelId"), "$or": scope},
		bson.M{"$set": bson.M{"timestamps.deleted_at": now}},
	)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ---------- Page Serving ----------

func handleGetPageByDomainPath(c *gin.Context) {
	domain := c.Query("domain")
	path := c.Query("path")
	if domain == "" || path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "domain and path are required"})
		return
	}

	var funnel pkgmodels.Funnel
	err := db.GetCollection(pkgmodels.FunnelCollection).Find(bson.M{
		"domain":                domain,
		"timestamps.deleted_at": nil,
	}).One(&funnel)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "funnel not found for domain"})
		return
	}

	for _, route := range funnel.Routes {
		for _, stage := range route.Stages {
			if stage.Path == path {
				if len(stage.Pages) > 0 {
					page := stage.Pages[0]
					var triggerInfos []gin.H
					for _, trigger := range stage.Triggers {
						ti := gin.H{
							"trigger_type": trigger.TriggerType,
							"public_id":    trigger.PublicId,
						}
						if trigger.UserActionValue != "" {
							ti["user_action_value"] = trigger.UserActionValue
						}
						if trigger.WatchBlockID != "" {
							ti["watch_block_id"] = trigger.WatchBlockID
							ti["watch_operator"] = trigger.WatchOperator
							ti["watch_percent"] = trigger.WatchPercent
						}
						triggerInfos = append(triggerInfos, ti)
					}
					c.JSON(http.StatusOK, gin.H{
						"status": "ok",
						"page":   page,
						"stage": gin.H{
							"name":      stage.Name,
							"path":      stage.Path,
							"public_id": stage.PublicId,
							"triggers":  triggerInfos,
						},
						"funnel": gin.H{
							"name":      funnel.Name,
							"domain":    funnel.Domain,
							"public_id": funnel.PublicId,
						},
					})
					return
				}
			}
		}
	}

	c.JSON(http.StatusNotFound, gin.H{"error": "page not found for path"})
}

// ---------- Event Handling ----------

func handleFunnelEvent(c *gin.Context) {
	var req struct {
		EventType   string            `json:"event_type"`
		FormName    string            `json:"form_name"`
		StageId     string            `json:"stage_id"`
		FunnelId    string            `json:"funnel_id"`
		UserId      string            `json:"user_id"`
		Data        map[string]string `json:"data"`
		BlockId     string            `json:"block_id"`
		ProgressPct int               `json:"progress_pct"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	// ACQ-001: the event must carry a tenant-resolvable funnel identity —
	// never search all tenants' funnels for a matching stage. The funnel row
	// is the tenant anchor; the stage must belong to THAT funnel, and the
	// acting user must belong to the same tenant.
	if req.FunnelId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "funnel_id is required"})
		return
	}
	var funnel pkgmodels.Funnel
	if err := db.GetCollection(pkgmodels.FunnelCollection).Find(bson.M{
		"public_id":             req.FunnelId,
		"timestamps.deleted_at": nil,
	}).One(&funnel); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "funnel not found"})
		return
	}
	tenantHex := funnel.SubscriberId
	if funnel.TenantID.Valid() {
		tenantHex = funnel.TenantID.Hex()
	}
	var stage *pkgmodels.FunnelStage
	for ri := range funnel.Routes {
		for si := range funnel.Routes[ri].Stages {
			s := funnel.Routes[ri].Stages[si]
			if s != nil && (req.StageId == "" || s.PublicId == req.StageId || s.Path == req.StageId) {
				stage = s
				break
			}
		}
		if stage != nil {
			break
		}
	}
	if stage == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "stage not found"})
		return
	}
	// The acting user must exist inside the funnel's tenant — a user public
	// id from another tenant is rejected, not silently acted on.
	if req.UserId != "" {
		n, _ := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
			"public_id": req.UserId,
			"$or": []bson.M{
				{"subscriber_id": tenantHex},
				{"tenant_id": bson.ObjectIdHex(tenantHex)},
			},
		}).Count()
		if n == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
	}

	var triggerType string
	switch req.EventType {
	case "submit":
		triggerType = pkgmodels.OnSubmit
	case "abandon":
		triggerType = pkgmodels.OnAbandon
	case "purchase":
		triggerType = pkgmodels.OnPurchase
	case "video_progress":
		triggerType = pkgmodels.OnWatch
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid event_type"})
		return
	}

	var commands []gin.H
	for _, trigger := range stage.Triggers {
		if trigger.TriggerType == triggerType {
			if req.FormName != "" && trigger.UserActionValue != "" && trigger.UserActionValue != req.FormName {
				continue
			}
			if triggerType == pkgmodels.OnWatch && trigger.WatchBlockID != "" {
				if trigger.WatchBlockID != req.BlockId {
					continue
				}
				if !evaluateWatchThreshold(trigger.WatchOperator, req.ProgressPct, trigger.WatchPercent) {
					continue
				}
			}
			if trigger.DoAction != nil {
				actionResult := executeFunnelAction(tenantHex, trigger.DoAction, req.UserId, req.FunnelId)
				commands = append(commands, actionResult...)
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"commands": commands,
		"user_id":  req.UserId,
	})
}

func executeFunnelAction(tenantHex string, action *pkgmodels.Action, userId string, funnelId string) []gin.H {
	var commands []gin.H
	if action == nil || tenantHex == "" {
		return commands
	}

	log.Printf("executeFunnelAction: action=%s extraActions=%v userId=%q tenant=%s", action.ActionName, action.ExtraActions, userId, tenantHex)

	// ACQ-002: every badge/user mutation is scoped to the funnel's resolved
	// tenant. Badge and EmailList rows vary between tenant_id (ObjectId) and
	// subscriber_id (string) generations, so both keys are matched — but
	// always bound to THIS tenant.
	tenantScope := []bson.M{{"subscriber_id": tenantHex}}
	if bson.IsObjectIdHex(tenantHex) {
		tenantScope = append(tenantScope, bson.M{"tenant_id": bson.ObjectIdHex(tenantHex)})
	}

	// parseColonCmd splits "type:value" into ("type", "value").
	// If there is no colon the value is empty.
	parseColonCmd := func(s string) (string, string) {
		for i, c := range s {
			if c == ':' {
				return s[:i], s[i+1:]
			}
		}
		return s, ""
	}

	applyBadge := func(badgeName string) {
		var badge pkgmodels.Badge
		if err := db.GetCollection(pkgmodels.BadgeCollection).Find(bson.M{"name": badgeName, "$or": tenantScope}).One(&badge); err == nil {
			db.GetCollection(pkgmodels.UserCollection).Update(
				bson.M{"public_id": userId, "$or": tenantScope},
				bson.M{"$addToSet": bson.M{"badges": badge.Id}},
			)
			log.Printf("executeFunnelAction: gave badge %q to user %q", badgeName, userId)
		} else {
			log.Printf("executeFunnelAction: badge %q not found in tenant %s: %v", badgeName, tenantHex, err)
		}
	}

	revokeBadge := func(badgeName string) {
		var badge pkgmodels.Badge
		if err := db.GetCollection(pkgmodels.BadgeCollection).Find(bson.M{"name": badgeName, "$or": tenantScope}).One(&badge); err == nil {
			db.GetCollection(pkgmodels.UserCollection).Update(
				bson.M{"public_id": userId, "$or": tenantScope},
				bson.M{"$pull": bson.M{"badges": badge.Id}},
			)
		}
	}

	// 1. Process badge transactions first (give / remove badges).
	if action.BadgeTransaction != nil {
		for _, b := range action.BadgeTransaction.GiveBadges {
			if b != nil {
				applyBadge(b.Name)
				commands = append(commands, gin.H{"action": "give_badge", "badge": b.Name})
			}
		}
		for _, b := range action.BadgeTransaction.RemoveBadges {
			if b != nil {
				revokeBadge(b.Name)
				commands = append(commands, gin.H{"action": "remove_badge", "badge": b.Name})
			}
		}
	}

	// 2. Execute the primary action (ActionName may be "type:value").
	execCmd := func(raw string) {
		typ, val := parseColonCmd(raw)
		switch typ {
		case "give_badge":
			if val != "" {
				applyBadge(val)
				commands = append(commands, gin.H{"action": "give_badge", "badge": val})
			}
		case "remove_badge":
			if val != "" {
				revokeBadge(val)
				commands = append(commands, gin.H{"action": "remove_badge", "badge": val})
			}
		case "start_story":
			commands = append(commands, gin.H{"action": "start_story", "story": val})
			// ACQ-003: persist a durable story-start command (retried,
			// dead-lettered) instead of an untracked goroutine.
			if val != "" && userId != "" {
				if err := EnqueueStoryStart(tenantHex, val, userId); err != nil {
					log.Printf("executeFunnelAction: enqueue story start failed: %v", err)
				}
			}
		case "jump_to_stage":
			commands = append(commands, gin.H{"action": "jump_to_stage", "stage": val})
		case "mark_complete", "next_scene", "advance_to_next_storyline":
			commands = append(commands, gin.H{"action": typ})
		default:
			if raw != "" {
				commands = append(commands, gin.H{"action": raw})
			}
		}
	}

	execCmd(action.ActionName)

	// 3. Execute extra actions.
	for _, ea := range action.ExtraActions {
		execCmd(ea)
	}

	return commands
}

func evaluateWatchThreshold(operator string, progressPct int, threshold int) bool {
	switch operator {
	case ">":
		return progressPct > threshold
	case ">=":
		return progressPct >= threshold
	case "<":
		return progressPct < threshold
	case "<=":
		return progressPct <= threshold
	default:
		return progressPct >= threshold
	}
}

// ---------- Funnel Template CRUD ----------

func handleGetFunnelTemplates(c *gin.Context) {
	scope := jwtTenantOr(c)
	if scope == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var templates []pkgmodels.FunnelTemplate
	db.GetCollection(pkgmodels.FunnelTemplateCollection).Find(bson.M{
		"$or":                   scope,
		"timestamps.deleted_at": nil,
	}).All(&templates)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "templates": templates})
}

func handleGetFunnelTemplate(c *gin.Context) {
	scope := jwtTenantOr(c)
	if scope == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var tmpl pkgmodels.FunnelTemplate
	err := db.GetCollection(pkgmodels.FunnelTemplateCollection).Find(bson.M{"public_id": c.Param("templateId"), "$or": scope}).One(&tmpl)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "template not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "template": tmpl})
}

func handleCreateFunnelTemplate(c *gin.Context) {
	sId := pkgauth.GetTenantID(c)
	if sId == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var tmpl pkgmodels.FunnelTemplate
	if err := c.ShouldBindJSON(&tmpl); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid template data"})
		return
	}
	now := time.Now()
	tmpl.Id = bson.NewObjectId()
	tmpl.PublicId = utils.GeneratePublicId()
	tmpl.SubscriberId = sId
	tmpl.TenantID = pkgauth.GetTenantObjectID(c)
	tmpl.SoftDeletes.CreatedAt = &now
	db.GetCollection(pkgmodels.FunnelTemplateCollection).Insert(tmpl)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "template": tmpl})
}

func handleUpdateFunnelTemplate(c *gin.Context) {
	templateId := c.Param("templateId")
	var updates struct {
		Name                 string                          `json:"name"`
		TemplateKind         string                          `json:"template_kind"`
		DefaultForTenant     *bool                           `json:"default_for_tenant"`
		DefaultForPageType   string                          `json:"default_for_page_type"`
		HTMLContent          string                          `json:"html_content"`
		GlobalCSS            string                          `json:"global_css"`
		SlotManifest         *pkgmodels.SlotManifest         `json:"slot_manifest"`
		MasterPrompt         string                          `json:"master_prompt"`
		InputSchema          bson.M                          `json:"input_schema"`
		ExpectedOutputSchema bson.M                          `json:"expected_output_schema"`
		StyleProfile         *pkgmodels.TemplateStyleProfile `json:"style_profile"`
		AssetManifest        []pkgmodels.TemplateAsset       `json:"asset_manifest"`
	}
	if err := c.ShouldBindJSON(&updates); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid data"})
		return
	}
	set := bson.M{}
	if updates.Name != "" {
		set["name"] = updates.Name
	}
	if updates.TemplateKind != "" {
		set["template_kind"] = updates.TemplateKind
	}
	if updates.DefaultForPageType != "" {
		set["default_for_page_type"] = updates.DefaultForPageType
	}
	if updates.DefaultForTenant != nil {
		set["default_for_tenant"] = *updates.DefaultForTenant
	}
	if updates.HTMLContent != "" {
		set["html_content"] = updates.HTMLContent
	}
	if updates.GlobalCSS != "" {
		set["global_css"] = updates.GlobalCSS
	}
	if updates.SlotManifest != nil {
		set["slot_manifest"] = updates.SlotManifest
	}
	if updates.MasterPrompt != "" {
		set["master_prompt"] = updates.MasterPrompt
	}
	if updates.InputSchema != nil {
		set["input_schema"] = updates.InputSchema
	}
	if updates.ExpectedOutputSchema != nil {
		set["expected_output_schema"] = updates.ExpectedOutputSchema
	}
	if updates.StyleProfile != nil {
		set["style_profile"] = updates.StyleProfile
	}
	if updates.AssetManifest != nil {
		set["asset_manifest"] = updates.AssetManifest
	}
	scope := jwtTenantOr(c)
	if scope == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	if len(set) > 0 {
		db.GetCollection(pkgmodels.FunnelTemplateCollection).Update(
			bson.M{"public_id": templateId, "$or": scope},
			bson.M{"$set": set},
		)
		// If this template is now the tenant default, clear the flag on
		// every other template of the same kind for the tenant. Read the
		// updated record to get the tenant id (the route doesn't carry it
		// in the URL).
		if updates.DefaultForTenant != nil && *updates.DefaultForTenant {
			var t pkgmodels.FunnelTemplate
			if err := db.GetCollection(pkgmodels.FunnelTemplateCollection).Find(bson.M{
				"public_id": templateId,
			}).One(&t); err == nil && t.TenantID != "" {
				_, _ = db.GetCollection(pkgmodels.FunnelTemplateCollection).UpdateAll(
					bson.M{
						"tenant_id":     t.TenantID,
						"template_kind": t.TemplateKind,
						"public_id":     bson.M{"$ne": templateId},
					},
					bson.M{"$set": bson.M{"default_for_tenant": false}},
				)
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleDeleteFunnelTemplate(c *gin.Context) {
	scope := jwtTenantOr(c)
	if scope == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	now := time.Now()
	db.GetCollection(pkgmodels.FunnelTemplateCollection).Update(
		bson.M{"public_id": c.Param("templateId"), "$or": scope},
		bson.M{"$set": bson.M{"timestamps.deleted_at": now}},
	)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
