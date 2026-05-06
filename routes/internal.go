package routes

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterInternalRoutes registers internal-only endpoints (no auth).
func RegisterInternalRoutes(rg *gin.RouterGroup) {
	rg.POST("/hydrate-funnel", HandleInternalHydrateFunnel)
	rg.POST("/hydrate-graph", HandleInternalHydrateGraph)
	rg.POST("/test/backdate-enrollment", HandleInternalBackdateEnrollment)
}

// RegisterInternalE2ETestRoutes registers test-only endpoints under a caller-
// chosen prefix. Used to expose the e2e helpers via Caddy on /api/marketing/test
// in addition to the internal /internal/test paths.
func RegisterInternalE2ETestRoutes(rg *gin.RouterGroup) {
	rg.POST("/backdate-enrollment", HandleInternalBackdateEnrollment)
}

// HandleInternalBackdateEnrollment shifts an enrollment's EnrolledAt backwards
// so e2e tests can prove drip windows release without waiting real time.
// Gated by SENTANYL_E2E_MODE=1 to keep it inert in production.
func HandleInternalBackdateEnrollment(c *gin.Context) {
	if os.Getenv("SENTANYL_E2E_MODE") != "1" {
		c.JSON(http.StatusForbidden, gin.H{"error": "e2e mode disabled"})
		return
	}
	var req struct {
		EnrollmentPublicID string `json:"enrollment_public_id" binding:"required"`
		ShiftMinutes       int    `json:"shift_minutes" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	col := db.GetCollection(pkgmodels.CourseEnrollmentCollection)
	var e pkgmodels.CourseEnrollment
	if err := col.Find(bson.M{"public_id": req.EnrollmentPublicID}).One(&e); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "enrollment not found"})
		return
	}
	newEnrolledAt := e.EnrolledAt.Add(-time.Duration(req.ShiftMinutes) * time.Minute)
	if err := col.UpdateId(e.Id, bson.M{"$set": bson.M{"enrolled_at": newEnrolledAt}}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"enrolled_at": newEnrolledAt.Format(time.RFC3339)})
}

// HydrateFunnelRequest is the payload for the internal hydration endpoint.
type HydrateFunnelRequest struct {
	Funnel *pkgmodels.Funnel `json:"funnel"`
	Story  *pkgmodels.Story  `json:"story"`
}

// HydrateGraphRequest is the generalized payload accepted by
// /internal/hydrate-graph. It mirrors the full surface of
// scripting.CompileResult so the SentanylScript compiler can deploy
// everything it knows how to emit, not just the funnel/story subset.
//
// Each top-level slice is upserted by either ObjectId (if present) or by
// (tenant_id, public_id) so re-running the same script is idempotent.
// Anything left unrecognized lands in the `unknown_entities` collection
// per resolveCollection's fallback to keep loose ends visible during
// development rather than silently dropped.
type HydrateGraphRequest struct {
	Funnels  []*pkgmodels.Funnel  `json:"funnels"`
	Stories  []*pkgmodels.Story   `json:"stories"`
	Sites    []*pkgmodels.Site    `json:"sites"`
	Products []*pkgmodels.Product `json:"products"`
	Offers   []*pkgmodels.Offer   `json:"offers"`
	Assets   []*pkgmodels.Asset   `json:"assets"`

	MediaEntities []*pkgmodels.Media         `json:"media_entities"`
	PlayerPresets []*pkgmodels.PlayerPreset  `json:"player_presets"`
	Channels      []*pkgmodels.MediaChannel  `json:"channels"`
	MediaWebhooks []*pkgmodels.MediaWebhook  `json:"media_webhooks"`

	Quizzes              []*pkgmodels.LMSQuiz             `json:"quizzes"`
	CertificateTemplates []*pkgmodels.CertificateTemplate `json:"certificate_templates"`

	Campaigns []*pkgmodels.Campaign `json:"campaigns"`

	Badges []*pkgmodels.Badge `json:"badges"`
	Tags   []*pkgmodels.Tag   `json:"tags"`
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

// insertEntities upserts each entity into its appropriate MongoDB collection.
// Upsert (not Insert) so that:
//  1. Re-running the same script doesn't fail with duplicate-key errors.
//  2. The same Badge referenced by multiple BadgeTransactions in one
//     decomposed funnel/story tree only writes once (the chain emits the
//     Badge struct per reference; mgo's UpsertId is idempotent).
// For entities lacking a usable _id, fall back to Insert.
func insertEntities(entities []interface{}) error {
	for _, entity := range entities {
		collection := resolveCollection(entity)
		col := db.GetCollection(collection)
		id, _, _, hasID := extractIdentity(entity)
		if hasID && id.Valid() {
			if _, err := col.UpsertId(id, bson.M{"$set": entity}); err != nil {
				log.Printf("hydrate-graph: upsert into %s failed for type %T: %v", collection, entity, err)
				return err
			}
			continue
		}
		if err := col.Insert(entity); err != nil {
			log.Printf("hydrate-graph: insert into %s failed for type %T: %v", collection, entity, err)
			return err
		}
	}
	return nil
}

// HandleInternalHydrateGraph is the generalized hydrator that accepts every
// entity type the compiler can emit. Each entity is upserted: by _id if
// the compiler set one, otherwise by (tenant_id, public_id) when present.
// Re-running the same script therefore refreshes existing rows rather than
// duplicating them, mirroring how /hydrate-funnel already handled the
// funnel/story subset.
func HandleInternalHydrateGraph(c *gin.Context) {
	var req HydrateGraphRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	totals := map[string]int{}

	// Funnels and Stories use the existing decompose+insert path so we keep
	// trigger/action/scene/message side-tables in sync.
	for _, f := range req.Funnels {
		if f == nil {
			continue
		}
		entities := f.ReadyMongoStore()
		if err := insertEntities(entities); err != nil {
			log.Printf("hydrate-graph: funnel insert failed: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to insert funnel"})
			return
		}
		totals["funnel_entities"] += len(entities)
	}
	for _, s := range req.Stories {
		if s == nil {
			continue
		}
		entities := s.ReadyMongoStore()
		if err := insertEntities(entities); err != nil {
			log.Printf("hydrate-graph: story insert failed: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to insert story"})
			return
		}
		totals["story_entities"] += len(entities)
	}

	// Top-level entity types are upserted directly into their canonical
	// collections. Each helper resolves a meaningful identity (either the
	// caller-supplied _id or the (tenant, public_id) pair).
	upsertSlice(&totals, "sites", pkgmodels.SiteCollection, req.Sites, sliceToAny(req.Sites))
	upsertSlice(&totals, "products", pkgmodels.ProductCollection, req.Products, sliceToAny(req.Products))
	upsertSlice(&totals, "offers", pkgmodels.OfferCollection, req.Offers, sliceToAny(req.Offers))
	upsertSlice(&totals, "assets", pkgmodels.AssetCollection, req.Assets, sliceToAny(req.Assets))
	upsertSlice(&totals, "media_entities", pkgmodels.MediaCollection, req.MediaEntities, sliceToAny(req.MediaEntities))
	upsertSlice(&totals, "player_presets", pkgmodels.PlayerPresetCollection, req.PlayerPresets, sliceToAny(req.PlayerPresets))
	upsertSlice(&totals, "channels", pkgmodels.MediaChannelCollection, req.Channels, sliceToAny(req.Channels))
	upsertSlice(&totals, "media_webhooks", pkgmodels.MediaWebhookCollection, req.MediaWebhooks, sliceToAny(req.MediaWebhooks))
	upsertSlice(&totals, "quizzes", pkgmodels.LMSQuizCollection, req.Quizzes, sliceToAny(req.Quizzes))
	upsertSlice(&totals, "certificate_templates", pkgmodels.CertificateTemplateCollection, req.CertificateTemplates, sliceToAny(req.CertificateTemplates))
	upsertSlice(&totals, "campaigns", pkgmodels.CampaignCollection, req.Campaigns, sliceToAny(req.Campaigns))
	upsertSlice(&totals, "badges", pkgmodels.BadgeCollection, req.Badges, sliceToAny(req.Badges))
	upsertSlice(&totals, "tags", pkgmodels.TagCollection, req.Tags, sliceToAny(req.Tags))

	log.Printf("hydrate-graph: counts=%v", totals)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "counts": totals})
}

func sliceToAny[T any](in []T) []interface{} {
	if len(in) == 0 {
		return nil
	}
	out := make([]interface{}, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	return out
}

// upsertSlice writes each entity in `items` into `coll` keyed off its _id
// when set, falling back to (tenant_id, public_id) when both are present.
// Entities lacking both keys fall through to a plain insert. Errors are
// logged per-row but don't abort the whole hydration so a single bad row
// can't poison a deploy.
func upsertSlice[T any](totals *map[string]int, label, coll string, _ []T, items []interface{}) {
	for _, raw := range items {
		if raw == nil {
			continue
		}
		id, tenantID, publicID, ok := extractIdentity(raw)
		c := db.GetCollection(coll)
		var err error
		switch {
		case ok && id.Valid():
			_, err = c.UpsertId(id, bson.M{"$set": raw})
		case publicID != "" && tenantID.Valid():
			_, err = c.Upsert(bson.M{"public_id": publicID, "tenant_id": tenantID}, bson.M{"$set": raw})
		case publicID != "":
			_, err = c.Upsert(bson.M{"public_id": publicID}, bson.M{"$set": raw})
		default:
			err = c.Insert(raw)
		}
		if err != nil {
			log.Printf("hydrate-graph: upsert %s failed: %v", label, err)
			continue
		}
		(*totals)[label]++
	}
}

// extractIdentity uses reflection-free type assertions to read _id, TenantID,
// and PublicId from any of the entity types we hydrate. Returning ok=false
// for the id slot tells the caller to fall back to the (tenant, public_id)
// path. The cases here MUST be kept in sync with HydrateGraphRequest.
func extractIdentity(raw interface{}) (id, tenantID bson.ObjectId, publicID string, hasID bool) {
	switch v := raw.(type) {
	case *pkgmodels.Site:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case *pkgmodels.Product:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case *pkgmodels.Offer:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case *pkgmodels.Asset:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case *pkgmodels.Media:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case *pkgmodels.PlayerPreset:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case *pkgmodels.MediaChannel:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case *pkgmodels.MediaWebhook:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case *pkgmodels.LMSQuiz:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case *pkgmodels.CertificateTemplate:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case *pkgmodels.Campaign:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case *pkgmodels.Badge:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case *pkgmodels.Tag:
		// Tag predates the tenant_id column and is scoped by subscriber_id.
		// Fall back to ObjectId-only upsert.
		return v.Id, "", v.PublicId, v.Id.Valid()
	// Value-type cases — funnel / story decompose paths emit value types
	// (`funnel := *f; individuals = append(individuals, funnel)`) that the
	// generic upsert in insertEntities also needs to identify by _id so it
	// can re-run idempotently.
	case pkgmodels.Funnel:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case pkgmodels.FunnelRoute:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case pkgmodels.FunnelStage:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case pkgmodels.FunnelPage:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case pkgmodels.PageBlock:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case pkgmodels.PageForm:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case pkgmodels.Trigger:
		return v.Id, "", v.PublicId, v.Id.Valid()
	case pkgmodels.Action:
		return v.Id, "", v.PublicId, v.Id.Valid()
	case pkgmodels.BadgeTransaction:
		return v.Id, "", "", v.Id.Valid()
	case pkgmodels.RequiredBadge:
		return v.Id, "", "", v.Id.Valid()
	case pkgmodels.Badge:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case pkgmodels.Story:
		return v.Id, v.TenantID, v.PublicId, v.Id.Valid()
	case pkgmodels.Storyline:
		return v.Id, "", v.PublicId, v.Id.Valid()
	case pkgmodels.Enactment:
		return v.Id, "", v.PublicId, v.Id.Valid()
	case pkgmodels.Scene:
		return v.Id, "", v.PublicId, v.Id.Valid()
	case pkgmodels.Message:
		return v.Id, "", v.PublicId, v.Id.Valid()
	case pkgmodels.MessageContent:
		return v.Id, "", v.PublicId, v.Id.Valid()
	}
	return "", "", "", false
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
	case pkgmodels.Badge:
		return pkgmodels.BadgeCollection
	default:
		log.Printf("resolveCollection: unknown entity type %T, using default", entity)
		return "unknown_entities"
	}
}
