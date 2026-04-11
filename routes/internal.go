package routes

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/josephalai/sentanyl/marketing-service/models"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterInternalRoutes registers internal-only endpoints (no auth).
func RegisterInternalRoutes(rg *gin.RouterGroup) {
	rg.POST("/hydrate-funnel", HandleInternalHydrateFunnel)
}

// HydrateFunnelRequest is the payload for the internal hydration endpoint.
type HydrateFunnelRequest struct {
	Funnel *models.Funnel `json:"funnel"`
	Story  *models.Story  `json:"story"`
}

// HandleInternalHydrateFunnel accepts hydrated Funnel and Story data from the
// compiler service, decomposes them via ReadyMongoStore, and inserts all
// individual entities into MongoDB.
func HandleInternalHydrateFunnel(c *gin.Context) {
	var req HydrateFunnelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if req.Funnel != nil {
		entities := req.Funnel.ReadyMongoStore()
		if err := insertEntities(entities); err != nil {
			log.Printf("hydrate-funnel: error inserting funnel entities: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to insert funnel"})
			return
		}
		log.Printf("hydrate-funnel: inserted %d funnel entities", len(entities))
	}

	if req.Story != nil {
		entities := req.Story.ReadyMongoStore()
		if err := insertEntities(entities); err != nil {
			log.Printf("hydrate-funnel: error inserting story entities: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to insert story"})
			return
		}
		log.Printf("hydrate-funnel: inserted %d story entities", len(entities))
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// insertEntities inserts each entity into its appropriate MongoDB collection
// based on its type. For types not explicitly mapped, it uses a default
// collection as a fallback.
func insertEntities(entities []interface{}) error {
	for _, entity := range entities {
		collection := resolveCollection(entity)
		if err := db.GetCollection(collection).Insert(entity); err != nil {
			return err
		}
	}
	return nil
}

// resolveCollection determines the MongoDB collection name for a given entity.
func resolveCollection(entity interface{}) string {
	switch entity.(type) {
	case models.Funnel:
		return pkgmodels.FunnelCollection
	case models.FunnelRoute:
		return pkgmodels.FunnelRouteCollection
	case models.FunnelStage:
		return pkgmodels.FunnelStageCollection
	case models.FunnelPage:
		return pkgmodels.FunnelPageCollection
	case models.PageBlock:
		return pkgmodels.PageBlockCollection
	case models.PageForm:
		return pkgmodels.PageFormCollection
	case models.Trigger:
		return pkgmodels.TriggerCollection
	case models.Action:
		return pkgmodels.ActionCollection
	case models.BadgeTransaction:
		return pkgmodels.BadgeTransactionCollection
	case models.RequiredBadge:
		return pkgmodels.BadgeCollection
	case models.Story:
		return pkgmodels.StoryCollection
	case models.Storyline:
		return pkgmodels.StorylineCollection
	case models.Enactment:
		return pkgmodels.EnactmentCollection
	case models.Scene:
		return pkgmodels.SceneCollection
	case models.Message:
		return pkgmodels.MessageCollection
	case models.MessageContent:
		return pkgmodels.MessageContentCollection
	case models.Tag:
		return pkgmodels.TagCollection
	default:
		log.Printf("resolveCollection: unknown entity type %T, using default", entity)
		return "unknown_entities"
	}
}
