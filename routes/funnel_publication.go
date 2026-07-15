package routes

import (
	"encoding/json"
	"log"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

func snapshotFunnelRoutes(routes []*pkgmodels.FunnelRoute) []*pkgmodels.FunnelRoute {
	if len(routes) == 0 {
		return nil
	}
	raw, _ := json.Marshal(routes)
	var out []*pkgmodels.FunnelRoute
	_ = json.Unmarshal(raw, &out)
	return out
}

// prepareCompiledFunnel treats a compiler deployment as an explicit publish:
// the complete hydrated graph is frozen into the root document before
// ReadyMongoStore decomposes the mutable authoring entities.
func prepareCompiledFunnel(f *pkgmodels.Funnel) {
	if f == nil {
		return
	}
	if f.DraftVersion < 1 {
		f.DraftVersion = 1
	}
	f.Status = "published"
	f.PublishedVersion = f.DraftVersion
	f.PublishedName = f.Name
	f.PublishedDomain = f.Domain
	f.PublishedRoutes = snapshotFunnelRoutes(f.Routes)
	now := time.Now().UTC()
	f.PublishedAt = &now
}

// EnsureFunnelPublicationState snapshots legacy funnels once so adding the
// publication gate does not take existing live pages offline. It is
// idempotent: only rows without a published snapshot are touched.
func EnsureFunnelPublicationState() {
	var funnels []pkgmodels.Funnel
	if err := db.GetCollection(pkgmodels.FunnelCollection).Find(bson.M{"$or": []bson.M{
		// Rows with no status predate the draft/publish model. A current draft
		// intentionally has no published_routes and must never be auto-published
		// on restart. The second arm only heals an older partial publish.
		{"status": bson.M{"$exists": false}},
		{"status": "published", "published_routes": bson.M{"$exists": false}},
	}}).All(&funnels); err != nil {
		log.Printf("funnel publication backfill scan: %v", err)
		return
	}
	for i := range funnels {
		f := &funnels[i]
		hydrateFunnelDraft(f)
		if len(f.Routes) == 0 {
			// Empty legacy funnels were never serveable; keep them draft.
			_ = db.GetCollection(pkgmodels.FunnelCollection).UpdateId(f.Id, bson.M{"$set": bson.M{"status": "draft", "draft_version": 1}})
			continue
		}
		prepareCompiledFunnel(f)
		if err := db.GetCollection(pkgmodels.FunnelCollection).UpdateId(f.Id, bson.M{"$set": bson.M{
			"status": f.Status, "draft_version": f.DraftVersion, "published_version": f.PublishedVersion,
			"published_name": f.PublishedName, "published_domain": f.PublishedDomain,
			"published_routes": f.PublishedRoutes, "published_at": f.PublishedAt,
		}}); err != nil {
			log.Printf("funnel publication backfill %s: %v", f.PublicId, err)
		}
	}
}

func hydrateFunnelDraft(f *pkgmodels.Funnel) {
	if f == nil || len(f.Routes) > 0 || f.RouteIds == nil {
		return
	}
	for _, routeID := range f.RouteIds.Ids {
		var route pkgmodels.FunnelRoute
		if db.GetCollection(pkgmodels.FunnelRouteCollection).FindId(routeID).One(&route) != nil {
			continue
		}
		if route.StageIds != nil {
			for _, stageID := range route.StageIds.Ids {
				var stage pkgmodels.FunnelStage
				if db.GetCollection(pkgmodels.FunnelStageCollection).FindId(stageID).One(&stage) != nil {
					continue
				}
				if stage.PageIds != nil {
					for _, pageID := range stage.PageIds.Ids {
						var page pkgmodels.FunnelPage
						if db.GetCollection(pkgmodels.FunnelPageCollection).FindId(pageID).One(&page) == nil {
							if page.BlockIds != nil {
								for _, id := range page.BlockIds.Ids {
									var block pkgmodels.PageBlock
									if db.GetCollection(pkgmodels.PageBlockCollection).FindId(id).One(&block) == nil {
										page.Blocks = append(page.Blocks, &block)
									}
								}
							}
							if page.FormIds != nil {
								for _, id := range page.FormIds.Ids {
									var form pkgmodels.PageForm
									if db.GetCollection(pkgmodels.PageFormCollection).FindId(id).One(&form) == nil {
										page.Forms = append(page.Forms, &form)
									}
								}
							}
							stage.Pages = append(stage.Pages, &page)
						}
					}
				}
				if stage.TriggerIds != nil {
					for _, id := range stage.TriggerIds.Ids {
						var trigger pkgmodels.Trigger
						if db.GetCollection(pkgmodels.TriggerCollection).FindId(id).One(&trigger) == nil {
							stage.Triggers = append(stage.Triggers, &trigger)
						}
					}
				}
				route.Stages = append(route.Stages, &stage)
			}
		}
		f.Routes = append(f.Routes, &route)
	}
}
