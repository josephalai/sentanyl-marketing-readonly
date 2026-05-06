// Command data-audit checks Sentanyl's MongoDB for the integration-state
// invariants Phase 7 cares about. Categories surfaced:
//
//   1. page_forms.on_submit.* references that point to non-existent badges,
//      lists, stories, downloads, products, or offers.
//   2. users.subscriber_id state vs users.tenant_id (legacy backfill gap).
//   3. subscriptions with empty/unknown status.
//   4. subscriptions without matching purchase_logs rows (revenue trail).
//   5. tenant_domains.is_verified vs sites.attached_domains parity.
//   6. funnel_templates count per template_kind (must hit a small threshold).
//
// Failures are emitted to stdout and the process exits non-zero so a CI
// hook can gate releases. Use --json to emit machine-readable output and
// --repair to apply the safe-by-default fixes (currently: drop dangling
// references from page_forms.on_submit.*).
//
// Usage:
//
//	go run ./marketing-service/cmd/data-audit \
//	   -mongo-host=localhost -mongo-port=27017 -mongo-db=sentanyl_db
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

type findings struct {
	OrphanFormReferences   map[string][]orphanRef `json:"orphan_form_references"`
	UsersMissingSubscriber int                    `json:"users_missing_subscriber"`
	UsersMissingTenant     int                    `json:"users_missing_tenant"`
	SubscriptionsBadStatus int                    `json:"subscriptions_bad_status"`
	SubscriptionsNoLog     int                    `json:"subscriptions_no_log"`
	UnverifiedDomains      []string               `json:"unverified_domains_attached_to_sites"`
	TemplatesByKind        map[string]int         `json:"funnel_templates_by_kind"`
}

type orphanRef struct {
	FormID    string `json:"form_id"`
	FormName  string `json:"form_name"`
	TenantID  string `json:"tenant_id"`
	Reference string `json:"reference"`
}

func main() {
	var (
		host    string
		port    string
		dbName  string
		jsonOut bool
		repair  bool
	)
	flag.StringVar(&host, "mongo-host", envOr("MONGO_HOST", "localhost"), "Mongo host")
	flag.StringVar(&port, "mongo-port", envOr("MONGO_PORT", "27017"), "Mongo port")
	flag.StringVar(&dbName, "mongo-db", envOr("MONGO_DB", "sentanyl_db"), "Mongo database name")
	flag.BoolVar(&jsonOut, "json", false, "Emit findings as JSON instead of human-readable text")
	flag.BoolVar(&repair, "repair", false, "Apply safe repairs (drops dangling on_submit references)")
	flag.Parse()

	db.MongoHost = host
	db.MongoPort = port
	db.MongoDB = dbName
	db.MongoDefaultCollectionName = "users"
	db.UsingLocalMongo = true
	db.InitMongoConnection()

	out := findings{
		OrphanFormReferences: map[string][]orphanRef{},
		TemplatesByKind:      map[string]int{},
	}
	auditFormReferences(&out, repair)
	auditUsers(&out)
	auditSubscriptions(&out)
	auditDomains(&out)
	auditTemplates(&out)

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	} else {
		printText(out)
	}

	if hasFailures(out) {
		os.Exit(1)
	}
}

func auditFormReferences(out *findings, repair bool) {
	var forms []pkgmodels.PageForm
	if err := db.GetCollection(pkgmodels.PageFormCollection).Find(bson.M{
		"timestamps.deleted_at": nil,
	}).All(&forms); err != nil {
		log.Printf("audit: list page_forms: %v", err)
		return
	}
	cache := newRefCache()
	for _, form := range forms {
		if form.OnSubmit == nil {
			continue
		}
		formRef := orphanRef{
			FormID:   form.PublicId,
			FormName: form.Name,
			TenantID: form.TenantID.Hex(),
		}
		check := func(category, ident string, ok bool) {
			if !ok {
				ref := formRef
				ref.Reference = ident
				out.OrphanFormReferences[category] = append(out.OrphanFormReferences[category], ref)
			}
		}
		for _, id := range form.OnSubmit.AssignBadgeIds {
			check("assign_badge_ids", id, cache.exists(form.TenantID, pkgmodels.BadgeCollection, id))
		}
		for _, id := range form.OnSubmit.RemoveBadgeIds {
			check("remove_badge_ids", id, cache.exists(form.TenantID, pkgmodels.BadgeCollection, id))
		}
		for _, id := range form.OnSubmit.AddToListIds {
			check("add_to_list_ids", id, cache.exists(form.TenantID, pkgmodels.EmailListCollection, id))
		}
		for _, id := range form.OnSubmit.RemoveFromListIds {
			check("remove_from_list_ids", id, cache.exists(form.TenantID, pkgmodels.EmailListCollection, id))
		}
		for _, id := range form.OnSubmit.StartStoryIds {
			check("start_story_ids", id, cache.exists(form.TenantID, pkgmodels.StoryCollection, id))
		}
		for _, id := range form.OnSubmit.DeliverDownloadIds {
			check("deliver_download_ids", id, cache.exists(form.TenantID, pkgmodels.ProductCollection, id))
		}
		for _, id := range form.OnSubmit.GrantProductIds {
			check("grant_product_ids", id, cache.exists(form.TenantID, pkgmodels.ProductCollection, id))
		}
		if id := form.OnSubmit.AttachOfferId; id != "" {
			check("attach_offer_id", id, cache.exists(form.TenantID, pkgmodels.OfferCollection, id))
		}
	}

	if repair && len(out.OrphanFormReferences) > 0 {
		repairFormReferences(out)
	}
}

type refCache struct {
	cache map[string]bool
}

func newRefCache() *refCache {
	return &refCache{cache: map[string]bool{}}
}

func (r *refCache) exists(tenantID bson.ObjectId, coll, publicID string) bool {
	if publicID == "" {
		return true // nothing to check
	}
	key := string(coll) + "|" + tenantID.Hex() + "|" + publicID
	if hit, ok := r.cache[key]; ok {
		return hit
	}
	n, err := db.GetCollection(coll).Find(bson.M{
		"public_id": publicID,
		"$or": []bson.M{
			{"tenant_id": tenantID},
			{"subscriber_id": tenantID.Hex()},
		},
		"timestamps.deleted_at": nil,
	}).Count()
	exists := err == nil && n > 0
	r.cache[key] = exists
	return exists
}

func repairFormReferences(out *findings) {
	// Group orphans by form id so each form is touched once.
	formSet := map[string]map[string]map[string]bool{}
	for category, refs := range out.OrphanFormReferences {
		for _, r := range refs {
			if _, ok := formSet[r.FormID]; !ok {
				formSet[r.FormID] = map[string]map[string]bool{}
			}
			if _, ok := formSet[r.FormID][category]; !ok {
				formSet[r.FormID][category] = map[string]bool{}
			}
			formSet[r.FormID][category][r.Reference] = true
		}
	}
	for formID, byCat := range formSet {
		var form pkgmodels.PageForm
		if err := db.GetCollection(pkgmodels.PageFormCollection).Find(bson.M{"public_id": formID}).One(&form); err != nil {
			continue
		}
		if form.OnSubmit == nil {
			continue
		}
		strip := func(in []string, drops map[string]bool) []string {
			if len(drops) == 0 {
				return in
			}
			out := in[:0]
			for _, v := range in {
				if !drops[v] {
					out = append(out, v)
				}
			}
			return out
		}
		form.OnSubmit.AssignBadgeIds = strip(form.OnSubmit.AssignBadgeIds, byCat["assign_badge_ids"])
		form.OnSubmit.RemoveBadgeIds = strip(form.OnSubmit.RemoveBadgeIds, byCat["remove_badge_ids"])
		form.OnSubmit.AddToListIds = strip(form.OnSubmit.AddToListIds, byCat["add_to_list_ids"])
		form.OnSubmit.RemoveFromListIds = strip(form.OnSubmit.RemoveFromListIds, byCat["remove_from_list_ids"])
		form.OnSubmit.StartStoryIds = strip(form.OnSubmit.StartStoryIds, byCat["start_story_ids"])
		form.OnSubmit.DeliverDownloadIds = strip(form.OnSubmit.DeliverDownloadIds, byCat["deliver_download_ids"])
		form.OnSubmit.GrantProductIds = strip(form.OnSubmit.GrantProductIds, byCat["grant_product_ids"])
		if byCat["attach_offer_id"][form.OnSubmit.AttachOfferId] {
			form.OnSubmit.AttachOfferId = ""
		}
		_ = db.GetCollection(pkgmodels.PageFormCollection).Update(
			bson.M{"_id": form.Id},
			bson.M{"$set": bson.M{"on_submit": form.OnSubmit}},
		)
	}
}

func auditUsers(out *findings) {
	col := db.GetCollection(pkgmodels.UserCollection)
	out.UsersMissingTenant, _ = col.Find(bson.M{
		"$or": []bson.M{
			{"tenant_id": bson.M{"$exists": false}},
			{"tenant_id": nil},
		},
	}).Count()
	out.UsersMissingSubscriber, _ = col.Find(bson.M{
		"$or": []bson.M{
			{"subscriber_id": bson.M{"$exists": false}},
			{"subscriber_id": ""},
		},
		"tenant_id": bson.M{"$exists": true},
	}).Count()
}

func auditSubscriptions(out *findings) {
	col := db.GetCollection(pkgmodels.SubscriptionCollection)
	out.SubscriptionsBadStatus, _ = col.Find(bson.M{
		"$or": []bson.M{
			{"status": bson.M{"$exists": false}},
			{"status": ""},
		},
	}).Count()

	// A subscription without a corresponding purchase_log entry is the most
	// common revenue-trail break. Match by stripe_session_id (canonical).
	var subs []pkgmodels.Subscription
	_ = col.Find(bson.M{}).All(&subs)
	for _, s := range subs {
		if s.StripeSessionID == "" {
			continue
		}
		n, _ := db.GetCollection(pkgmodels.PurchaseLogCollection).Find(bson.M{
			"stripe_session_id": s.StripeSessionID,
		}).Count()
		if n == 0 {
			out.SubscriptionsNoLog++
		}
	}
}

func auditDomains(out *findings) {
	var sites []pkgmodels.Site
	_ = db.GetCollection(pkgmodels.SiteCollection).Find(bson.M{
		"timestamps.deleted_at": nil,
	}).All(&sites)
	for _, site := range sites {
		for _, host := range site.AttachedDomains {
			n, _ := db.GetCollection(pkgmodels.DomainCollection).Find(bson.M{
				"hostname":    host,
				"is_verified": true,
			}).Count()
			if n == 0 {
				out.UnverifiedDomains = append(out.UnverifiedDomains, host)
			}
		}
	}
}

func auditTemplates(out *findings) {
	col := db.GetCollection(pkgmodels.FunnelTemplateCollection)
	for _, kind := range []string{
		pkgmodels.TemplateKindSqueezePage,
		pkgmodels.TemplateKindSalesPage,
		pkgmodels.TemplateKindCheckout,
		pkgmodels.TemplateKindThankYou,
		pkgmodels.TemplateKindWebinar,
		pkgmodels.TemplateKindLeadMagnet,
		pkgmodels.TemplateKindCustom,
	} {
		n, _ := col.Find(bson.M{"template_kind": kind, "timestamps.deleted_at": nil}).Count()
		out.TemplatesByKind[kind] = n
	}
}

func hasFailures(out findings) bool {
	if len(out.OrphanFormReferences) > 0 {
		return true
	}
	if out.UsersMissingTenant > 0 || out.SubscriptionsBadStatus > 0 || out.SubscriptionsNoLog > 0 {
		return true
	}
	if len(out.UnverifiedDomains) > 0 {
		return true
	}
	// Each kind must have at least one template; without that, AI generation
	// cannot fall back to a default for that kind.
	for _, kind := range []string{
		pkgmodels.TemplateKindSqueezePage,
		pkgmodels.TemplateKindSalesPage,
		pkgmodels.TemplateKindCheckout,
		pkgmodels.TemplateKindThankYou,
	} {
		if out.TemplatesByKind[kind] == 0 {
			return true
		}
	}
	return false
}

func printText(out findings) {
	fmt.Println("== Sentanyl data audit ==")
	if total := totalOrphans(out.OrphanFormReferences); total > 0 {
		fmt.Printf("orphan page_forms.on_submit references: %d\n", total)
		for cat, refs := range out.OrphanFormReferences {
			fmt.Printf("  %s: %d\n", cat, len(refs))
			for _, r := range refs {
				fmt.Printf("    form=%s tenant=%s ref=%s\n", r.FormID, r.TenantID, r.Reference)
			}
		}
	} else {
		fmt.Println("orphan page_forms references: 0")
	}
	fmt.Printf("users missing tenant_id: %d\n", out.UsersMissingTenant)
	fmt.Printf("users missing subscriber_id (with tenant_id): %d\n", out.UsersMissingSubscriber)
	fmt.Printf("subscriptions with empty status: %d\n", out.SubscriptionsBadStatus)
	fmt.Printf("subscriptions without purchase_log: %d\n", out.SubscriptionsNoLog)
	fmt.Printf("attached domains lacking verified tenant_domains row: %d\n", len(out.UnverifiedDomains))
	for _, h := range out.UnverifiedDomains {
		fmt.Printf("  %s\n", h)
	}
	fmt.Println("funnel_templates by kind:")
	for kind, n := range out.TemplatesByKind {
		fmt.Printf("  %s: %d\n", kind, n)
	}
}

func totalOrphans(m map[string][]orphanRef) int {
	t := 0
	for _, v := range m {
		t += len(v)
	}
	return t
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
