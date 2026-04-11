package routes

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	
)

// RegisterInternalRoutes registers internal-only endpoints (no auth).
func RegisterInternalRoutes(rg *gin.RouterGroup) {
	rg.POST("/hydrate-funnel", HandleInternalHydrateFunnel)
}

// HydrateFunnelRequest is the payload for the internal hydration endpoint.
type HydrateFunnelRequest struct {
	Funnel *pkgmodels.Funnel `json:"funnel"`
	Story  *pkgmodels.Story  `json:"story"`
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
	case pkgmodels.Funnel:
		return pkgmodels.FunnelCollection
	case pkgmodels.FunnelRoute:
		return pkgmodels.FunnelRouteCollection
	case pkgmodels.FunnelStage:
		return pkgmodels.FunnelStageCollection
	case pkgmodels.FunnelPage:
		return pkgmodels.FunnelPageCollection
	case pkgmodels.PageBlock:
		return pkgmodels.PageBlockCollection
	case pkgmodels.PageForm:
		return pkgmodels.PageFormCollection
	case pkgmodels.Trigger:
		return pkgmodels.TriggerCollection
	case pkgmodels.Action:
		return pkgmodels.ActionCollection
	case pkgmodels.BadgeTransaction:
		return pkgmodels.BadgeTransactionCollection
	case pkgmodels.RequiredBadge:
		return pkgmodels.BadgeCollection
	case pkgmodels.Story:
		return pkgmodels.StoryCollection
	case pkgmodels.Storyline:
		return pkgmodels.StorylineCollection
	case pkgmodels.Enactment:
		return pkgmodels.EnactmentCollection
	case pkgmodels.Scene:
		return pkgmodels.SceneCollection
	case pkgmodels.Message:
		return pkgmodels.MessageCollection
	case pkgmodels.MessageContent:
		return pkgmodels.MessageContentCollection
	case pkgmodels.Tag:
		return pkgmodels.TagCollection
	default:
		log.Printf("resolveCollection: unknown entity type %T, using default", entity)
		return "unknown_entities"
	}
}
