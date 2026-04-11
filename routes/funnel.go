package routes

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/models"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

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
	var funnels []models.Funnel
	db.GetCollection(pkgmodels.FunnelCollection).Find(bson.M{
		"subscriber_id":         subscriberId,
		"timestamps.deleted_at": nil,
	}).All(&funnels)
	for i := range funnels {
		funnels[i].Hydrate()
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "funnels": funnels})
}

func handleGetFunnel(c *gin.Context) {
	funnelId := c.Param("funnelId")
	var funnel models.Funnel
	err := db.GetCollection(pkgmodels.FunnelCollection).Find(bson.M{"public_id": funnelId}).One(&funnel)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "funnel not found"})
		return
	}
	funnel.Hydrate()
	c.JSON(http.StatusOK, gin.H{"status": "ok", "funnel": funnel})
}

func handleCreateFunnel(c *gin.Context) {
	var funnel models.Funnel
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

	var funnel models.Funnel
	err := db.GetCollection(pkgmodels.FunnelCollection).Find(bson.M{
		"domain":                domain,
		"timestamps.deleted_at": nil,
	}).One(&funnel)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "funnel not found for domain"})
		return
	}
	funnel.Hydrate()

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

	var stage models.FunnelStage
	err := db.GetCollection(pkgmodels.FunnelStageCollection).Find(bson.M{"public_id": req.StageId}).One(&stage)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "stage not found"})
		return
	}
	stage.Hydrate()

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

func executeFunnelAction(action *models.Action, userId string, funnelId string) []gin.H {
	var commands []gin.H
	if action == nil {
		return commands
	}

	// TODO: Port full action execution logic from monolith.
	// For now, return a basic command based on the action name.
	log.Printf("executeFunnelAction: action=%s userId=%q funnelId=%q", action.ActionName, userId, funnelId)

	commands = append(commands, gin.H{
		"action": action.ActionName,
	})

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
	var products []models.Product
	db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"subscriber_id":         subscriberId,
		"timestamps.deleted_at": nil,
	}).All(&products)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "products": products})
}

func handleGetProduct(c *gin.Context) {
	productId := c.Param("productId")
	var product models.Product
	err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{"public_id": productId}).One(&product)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "product": product})
}

func handleCreateProduct(c *gin.Context) {
	var product models.Product
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
	var purchases []models.PurchaseLog
	db.GetCollection(pkgmodels.PurchaseLogCollection).Find(bson.M{
		"subscriber_id": subscriberId,
	}).All(&purchases)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "purchases": purchases})
}

// ---------- Funnel Template CRUD ----------

func handleGetFunnelTemplates(c *gin.Context) {
	subscriberId := c.Query("subscriber_id")
	var templates []models.FunnelTemplate
	db.GetCollection(pkgmodels.FunnelTemplateCollection).Find(bson.M{
		"subscriber_id":         subscriberId,
		"timestamps.deleted_at": nil,
	}).All(&templates)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "templates": templates})
}

func handleGetFunnelTemplate(c *gin.Context) {
	templateId := c.Param("templateId")
	var tmpl models.FunnelTemplate
	err := db.GetCollection(pkgmodels.FunnelTemplateCollection).Find(bson.M{"public_id": templateId}).One(&tmpl)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "template not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "template": tmpl})
}

func handleCreateFunnelTemplate(c *gin.Context) {
	var tmpl models.FunnelTemplate
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
		Name        string `json:"name"`
		HTMLContent string `json:"html_content"`
		GlobalCSS   string `json:"global_css"`
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
