package routes

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// triggerStoryStart calls core-service to start executing a story for a user.
// Runs in a goroutine — fire and forget.
func triggerStoryStart(storyName, funnelId, userPublicId string) {
	// Look up subscriber_id from the user record.
	var user pkgmodels.User
	subscriberId := ""
	if err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{"public_id": userPublicId}).One(&user); err == nil {
		subscriberId = user.SubscriberId
	}
	if subscriberId == "" {
		// Fall back: try to get subscriber_id from a funnel if funnelId is set.
		if funnelId != "" {
			var funnel pkgmodels.Funnel
			if err := db.GetCollection(pkgmodels.FunnelCollection).Find(bson.M{"public_id": funnelId}).One(&funnel); err == nil {
				subscriberId = funnel.SubscriberId
			}
		}
	}
	if subscriberId == "" {
		log.Printf("triggerStoryStart: no subscriber_id for user %s, story=%s", userPublicId, storyName)
		return
	}

	coreURL := os.Getenv("CORE_SERVICE_URL")
	if coreURL == "" {
		coreURL = "http://core-service:8081"
	}
	payload, _ := json.Marshal(map[string]string{
		"story_name":     storyName,
		"subscriber_id":  subscriberId,
		"user_public_id": userPublicId,
	})
	resp, err := http.Post(coreURL+"/internal/story/start", "application/json", bytes.NewReader(payload))
	if err != nil {
		log.Printf("triggerStoryStart: error calling core-service: %v", err)
		return
	}
	defer resp.Body.Close()
	log.Printf("triggerStoryStart: story=%q user=%s → HTTP %d", storyName, userPublicId, resp.StatusCode)
}

// RegisterLegacyTenantFunnelRoutes wires the funnels + purchases list under /api/tenant/*
// so that frontend pages built against the old monolith paths keep working.
// Caddy routes /api/tenant/funnels* and /api/tenant/purchases* to this service.
func RegisterLegacyTenantFunnelRoutes(rg *gin.RouterGroup) {
	rg.GET("/funnels", handleGetFunnels)
	rg.GET("/funnels/:funnelId", handleGetFunnel)
	rg.POST("/funnels", handleCreateFunnel)
	rg.DELETE("/funnels/:funnelId", handleDeleteFunnel)
	rg.GET("/purchases", handleGetPurchases)
}

// RegisterLegacyFunnelTemplateRoutes wires funnel-template CRUD under /api/funnel/*
// to match the legacy path /api/funnel/template used by FunnelTemplatesPage.
func RegisterLegacyFunnelTemplateRoutes(rg *gin.RouterGroup) {
	rg.GET("/template", handleGetFunnelTemplates)
	rg.GET("/template/:templateId", handleGetFunnelTemplate)
	rg.POST("/template", handleCreateFunnelTemplate)
	rg.PUT("/template/:templateId", handleUpdateFunnelTemplate)
	rg.DELETE("/template/:templateId", handleDeleteFunnelTemplate)
}

// RegisterFunnelRoutes registers all funnel-related endpoints.
func RegisterFunnelRoutes(rg *gin.RouterGroup) {
	rg.GET("/funnels", handleGetFunnels)
	rg.GET("/funnels/:funnelId", handleGetFunnel)
	rg.POST("/funnels", handleCreateFunnel)
	rg.DELETE("/funnels/:funnelId", handleDeleteFunnel)

	rg.GET("/funnel/page", handleGetPageByDomainPath)
	rg.POST("/funnel/event", handleFunnelEvent)

	rg.GET("/products", handleGetProducts)
	rg.GET("/products/:productId", handleGetProduct)
	rg.POST("/products", handleCreateProduct)
	rg.DELETE("/products/:productId", handleDeleteProduct)

	rg.GET("/purchases", handleGetPurchases)

	rg.GET("/funnel-templates", handleGetFunnelTemplates)
	rg.GET("/funnel-templates/:templateId", handleGetFunnelTemplate)
	rg.POST("/funnel-templates", handleCreateFunnelTemplate)
	rg.PUT("/funnel-templates/:templateId", handleUpdateFunnelTemplate)
	rg.DELETE("/funnel-templates/:templateId", handleDeleteFunnelTemplate)
}

// ---------- Funnel CRUD ----------

func handleGetFunnels(c *gin.Context) {
	subscriberId := c.Query("subscriber_id")
	var funnels []pkgmodels.Funnel
	db.GetCollection(pkgmodels.FunnelCollection).Find(bson.M{
		"subscriber_id":         subscriberId,
		"timestamps.deleted_at": nil,
	}).All(&funnels)
	for range funnels {
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "funnels": funnels})
}

func handleGetFunnel(c *gin.Context) {
	funnelId := c.Param("funnelId")
	var funnel pkgmodels.Funnel
	err := db.GetCollection(pkgmodels.FunnelCollection).Find(bson.M{"public_id": funnelId}).One(&funnel)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "funnel not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "funnel": funnel})
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
	funnel.SoftDeletes.CreatedAt = &now
	db.GetCollection(pkgmodels.FunnelCollection).Insert(funnel)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "funnel": funnel})
}

func handleDeleteFunnel(c *gin.Context) {
	funnelId := c.Param("funnelId")
	now := time.Now()
	db.GetCollection(pkgmodels.FunnelCollection).Update(
		bson.M{"public_id": funnelId},
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

	// Stages are embedded inside Funnel → Routes → Stages; not in a separate collection.
	// Search funnels for the matching stage by public_id (falling back to a funnel_id hint).
	var stage *pkgmodels.FunnelStage
	funnelQuery := bson.M{"timestamps.deleted_at": nil}
	if req.FunnelId != "" {
		funnelQuery["public_id"] = req.FunnelId
	}
	var funnels []pkgmodels.Funnel
	db.GetCollection(pkgmodels.FunnelCollection).Find(funnelQuery).All(&funnels)
	for fi := range funnels {
		for ri := range funnels[fi].Routes {
			for si := range funnels[fi].Routes[ri].Stages {
				s := funnels[fi].Routes[ri].Stages[si]
				if s != nil && (req.StageId == "" || s.PublicId == req.StageId || s.Path == req.StageId) {
					stage = s
					break
				}
			}
			if stage != nil {
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
				actionResult := executeFunnelAction(trigger.DoAction, req.UserId, req.FunnelId)
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

func executeFunnelAction(action *pkgmodels.Action, userId string, funnelId string) []gin.H {
	var commands []gin.H
	if action == nil {
		return commands
	}

	log.Printf("executeFunnelAction: action=%s extraActions=%v userId=%q", action.ActionName, action.ExtraActions, userId)

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
		if err := db.GetCollection(pkgmodels.BadgeCollection).Find(bson.M{"name": badgeName}).One(&badge); err == nil {
			db.GetCollection(pkgmodels.UserCollection).Update(
				bson.M{"public_id": userId},
				bson.M{"$addToSet": bson.M{"badges": badge.Id}},
			)
			log.Printf("executeFunnelAction: gave badge %q to user %q", badgeName, userId)
		} else {
			log.Printf("executeFunnelAction: badge %q not found: %v", badgeName, err)
		}
	}

	revokeBadge := func(badgeName string) {
		var badge pkgmodels.Badge
		if err := db.GetCollection(pkgmodels.BadgeCollection).Find(bson.M{"name": badgeName}).One(&badge); err == nil {
			db.GetCollection(pkgmodels.UserCollection).Update(
				bson.M{"public_id": userId},
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
			// Fire story execution engine in core-service asynchronously.
			if val != "" && userId != "" {
				go triggerStoryStart(val, funnelId, userId)
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

// ---------- Product CRUD ----------

func handleGetProducts(c *gin.Context) {
	subscriberId := c.Query("subscriber_id")
	var products []pkgmodels.Product
	db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"subscriber_id":         subscriberId,
		"timestamps.deleted_at": nil,
	}).All(&products)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "products": products})
}

func handleGetProduct(c *gin.Context) {
	productId := c.Param("productId")
	var product pkgmodels.Product
	err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{"public_id": productId}).One(&product)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "product": product})
}

func handleCreateProduct(c *gin.Context) {
	var product pkgmodels.Product
	if err := c.ShouldBindJSON(&product); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product data"})
		return
	}
	now := time.Now()
	product.Id = bson.NewObjectId()
	product.PublicId = utils.GeneratePublicId()
	product.SoftDeletes.CreatedAt = &now
	db.GetCollection(pkgmodels.ProductCollection).Insert(product)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "product": product})
}

func handleDeleteProduct(c *gin.Context) {
	productId := c.Param("productId")
	now := time.Now()
	db.GetCollection(pkgmodels.ProductCollection).Update(
		bson.M{"public_id": productId},
		bson.M{"$set": bson.M{"timestamps.deleted_at": now}},
	)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ---------- Purchase Log ----------

func handleGetPurchases(c *gin.Context) {
	subscriberId := c.Query("subscriber_id")
	var purchases []pkgmodels.PurchaseLog
	db.GetCollection(pkgmodels.PurchaseLogCollection).Find(bson.M{
		"subscriber_id": subscriberId,
	}).All(&purchases)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "purchases": purchases})
}

// ---------- Funnel Template CRUD ----------

func handleGetFunnelTemplates(c *gin.Context) {
	subscriberId := c.Query("subscriber_id")
	var templates []pkgmodels.FunnelTemplate
	db.GetCollection(pkgmodels.FunnelTemplateCollection).Find(bson.M{
		"subscriber_id":         subscriberId,
		"timestamps.deleted_at": nil,
	}).All(&templates)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "templates": templates})
}

func handleGetFunnelTemplate(c *gin.Context) {
	templateId := c.Param("templateId")
	var tmpl pkgmodels.FunnelTemplate
	err := db.GetCollection(pkgmodels.FunnelTemplateCollection).Find(bson.M{"public_id": templateId}).One(&tmpl)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "template not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "template": tmpl})
}

func handleCreateFunnelTemplate(c *gin.Context) {
	var tmpl pkgmodels.FunnelTemplate
	if err := c.ShouldBindJSON(&tmpl); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid template data"})
		return
	}
	now := time.Now()
	tmpl.Id = bson.NewObjectId()
	tmpl.PublicId = utils.GeneratePublicId()
	tmpl.SoftDeletes.CreatedAt = &now
	db.GetCollection(pkgmodels.FunnelTemplateCollection).Insert(tmpl)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "template": tmpl})
}

func handleUpdateFunnelTemplate(c *gin.Context) {
	templateId := c.Param("templateId")
	var updates struct {
		Name         string                  `json:"name"`
		HTMLContent  string                  `json:"html_content"`
		GlobalCSS    string                  `json:"global_css"`
		SlotManifest *pkgmodels.SlotManifest `json:"slot_manifest"`
	}
	if err := c.ShouldBindJSON(&updates); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid data"})
		return
	}
	set := bson.M{}
	if updates.Name != "" {
		set["name"] = updates.Name
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
	if len(set) > 0 {
		db.GetCollection(pkgmodels.FunnelTemplateCollection).Update(
			bson.M{"public_id": templateId},
			bson.M{"$set": set},
		)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleDeleteFunnelTemplate(c *gin.Context) {
	templateId := c.Param("templateId")
	now := time.Now()
	db.GetCollection(pkgmodels.FunnelTemplateCollection).Update(
		bson.M{"public_id": templateId},
		bson.M{"$set": bson.M{"timestamps.deleted_at": now}},
	)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
