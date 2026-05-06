// Command import-lead-templates seeds FunnelTemplate records from the
// lead-pages-templates/templates/<slug> directory tree.
//
// Each subdirectory is expected to contain:
//   - template.manifest.json (slot definitions, classification, …)
//   - index.template.html    (Handlebars-rewritten HTML the materializer fills)
//
// Records are upserted by (tenant_id, source_template_id) so re-running the
// importer is idempotent. Dry-run mode lists what would change without
// touching MongoDB.
//
// Usage:
//
//	go run ./marketing-service/cmd/import-lead-templates \
//	   -tenant-id=<hex> \
//	   -source-dir=./lead-pages-templates/templates \
//	   [-dry-run]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

type rawSlot struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
	Label       string `json:"label"`
}

type rawClassification struct {
	PrimaryCategory string   `json:"primary_category"`
	FunnelStage     string   `json:"funnel_stage"`
	PageRole        string   `json:"page_role"`
	Intent          string   `json:"intent"`
	TitleGuess      string   `json:"title_guess"`
	Reasoning       []string `json:"reasoning_signals"`
}

type rawPage struct {
	SourceHTML   string    `json:"source_html"`
	TemplateHTML string    `json:"template_html"`
	PageRole     string    `json:"page_role"`
	TitleGuess   string    `json:"title_guess"`
	Slots        []rawSlot `json:"slots"`
}

type rawManifest struct {
	TemplateID       string                 `json:"template_id"`
	TemplateName     string                 `json:"template_name"`
	SourceFolder     string                 `json:"source_folder"`
	PrimaryEntryHTML string                 `json:"primary_entry_html"`
	Classification   rawClassification      `json:"classification"`
	Pages            []rawPage              `json:"pages"`
	StyleProfile     map[string]interface{} `json:"style_profile"`
	MasterPrompt     string                 `json:"master_prompt"`
}

func main() {
	var (
		tenantHex string
		sourceDir string
		dryRun    bool
		mongoHost string
		mongoPort string
		mongoDB   string
	)
	flag.StringVar(&tenantHex, "tenant-id", "", "Tenant ObjectId hex (required)")
	flag.StringVar(&sourceDir, "source-dir", "lead-pages-templates/templates", "Templates root dir")
	flag.BoolVar(&dryRun, "dry-run", false, "List would-be changes; don't write to Mongo")
	flag.StringVar(&mongoHost, "mongo-host", envOr("MONGO_HOST", "localhost"), "Mongo host")
	flag.StringVar(&mongoPort, "mongo-port", envOr("MONGO_PORT", "27017"), "Mongo port")
	flag.StringVar(&mongoDB, "mongo-db", envOr("MONGO_DB", "sentanyl"), "Mongo database name")
	flag.Parse()

	if tenantHex == "" || !bson.IsObjectIdHex(tenantHex) {
		log.Fatalf("-tenant-id must be a valid ObjectId hex string")
	}
	tenantID := bson.ObjectIdHex(tenantHex)

	abs, err := filepath.Abs(sourceDir)
	if err != nil {
		log.Fatalf("source dir: %v", err)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		log.Fatalf("read source dir %s: %v", abs, err)
	}

	if !dryRun {
		db.MongoHost = mongoHost
		db.MongoPort = mongoPort
		db.MongoDB = mongoDB
		db.MongoDefaultCollectionName = "funnels"
		db.UsingLocalMongo = true
		db.InitMongoConnection()
	}

	imported, skipped, errors := 0, 0, 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(abs, e.Name())
		manifestPath := filepath.Join(dir, "template.manifest.json")
		if _, err := os.Stat(manifestPath); err != nil {
			skipped++
			continue
		}
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			log.Printf("[%s] read manifest: %v", e.Name(), err)
			errors++
			continue
		}
		var m rawManifest
		if err := json.Unmarshal(raw, &m); err != nil {
			log.Printf("[%s] parse manifest: %v", e.Name(), err)
			errors++
			continue
		}
		if len(m.Pages) == 0 {
			log.Printf("[%s] manifest has no pages, skipping", e.Name())
			skipped++
			continue
		}
		page := m.Pages[0]
		htmlPath := filepath.Join(dir, page.TemplateHTML)
		if page.TemplateHTML == "" {
			htmlPath = filepath.Join(dir, "index.template.html")
		}
		htmlBytes, err := os.ReadFile(htmlPath)
		if err != nil {
			log.Printf("[%s] read template html (%s): %v", e.Name(), htmlPath, err)
			errors++
			continue
		}

		tmpl := buildTemplate(tenantID, m, page, string(htmlBytes))
		if dryRun {
			log.Printf("[%s] would upsert %s (kind=%s, slots=%d)",
				e.Name(), tmpl.Name, tmpl.TemplateKind, len(tmpl.SlotManifest.Slots))
			imported++
			continue
		}
		if err := upsertTemplate(&tmpl); err != nil {
			log.Printf("[%s] upsert: %v", e.Name(), err)
			errors++
			continue
		}
		imported++
	}
	log.Printf("done: imported=%d skipped=%d errors=%d (dry_run=%v)", imported, skipped, errors, dryRun)
}

// buildTemplate maps a parsed manifest into a FunnelTemplate. Sets sane
// defaults for fields the manifest doesn't carry directly so the resulting
// record passes validation when stored.
func buildTemplate(tenantID bson.ObjectId, m rawManifest, page rawPage, html string) pkgmodels.FunnelTemplate {
	slots := make([]pkgmodels.FunnelSlot, 0, len(page.Slots))
	for _, s := range page.Slots {
		slots = append(slots, pkgmodels.FunnelSlot{
			Key:         s.Key,
			Label:       s.Label,
			SlotType:    s.Type,
			Required:    s.Required,
			Description: s.Description,
		})
	}
	now := time.Now()
	tmpl := pkgmodels.FunnelTemplate{
		Id:               bson.NewObjectId(),
		PublicId:         utils.GeneratePublicId(),
		TenantID:         tenantID,
		Name:             m.TemplateName,
		TemplateKind:     mapKind(m.Classification.PageRole, m.Classification.PrimaryCategory),
		SourceTemplateID: m.TemplateID,
		SourceFolder:     m.SourceFolder,
		HTMLContent:      html,
		SlotManifest:     &pkgmodels.SlotManifest{Slots: slots},
		MasterPrompt:     deriveMasterPrompt(m, page),
	}
	if m.StyleProfile != nil {
		tmpl.StyleProfile = &pkgmodels.TemplateStyleProfile{
			Tone:               strFromMap(m.StyleProfile, "tone"),
			VisualStyle:        strFromMap(m.StyleProfile, "visual_style"),
			CopyStyle:          strFromMap(m.StyleProfile, "copy_style"),
			CTAStyle:           strFromMap(m.StyleProfile, "cta_style"),
			AudienceAssumption: strFromMap(m.StyleProfile, "audience_assumption"),
		}
	}
	tmpl.SoftDeletes.CreatedAt = &now
	return tmpl
}

func deriveMasterPrompt(m rawManifest, page rawPage) string {
	if m.MasterPrompt != "" {
		return m.MasterPrompt
	}
	role := page.PageRole
	if role == "" {
		role = m.Classification.PageRole
	}
	intent := m.Classification.Intent
	return fmt.Sprintf(
		"You are generating copy for a %s page (%s). Title: %q. Return a JSON object whose top-level `slots` key maps every slot key from the manifest to a value matching the slot's type. Do not invent new keys.",
		role, intent, m.Classification.TitleGuess,
	)
}

func mapKind(role, primary string) string {
	r := strings.ToLower(strings.TrimSpace(role))
	switch r {
	case "squeeze", "opt-in", "optin", "opt_in", "lead-capture":
		return pkgmodels.TemplateKindSqueezePage
	case "lead_magnet", "lead-magnet", "freebie":
		return pkgmodels.TemplateKindLeadMagnet
	case "webinar", "webinar_registration":
		return pkgmodels.TemplateKindWebinar
	case "checkout", "order_form":
		return pkgmodels.TemplateKindCheckout
	case "thank_you", "thank-you", "confirmation":
		return pkgmodels.TemplateKindThankYou
	case "sales", "sales_page":
		return pkgmodels.TemplateKindSalesPage
	}
	if strings.Contains(strings.ToLower(primary), "thank") {
		return pkgmodels.TemplateKindThankYou
	}
	if strings.Contains(strings.ToLower(primary), "webinar") {
		return pkgmodels.TemplateKindWebinar
	}
	return pkgmodels.TemplateKindCustom
}

func strFromMap(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// upsertTemplate inserts or updates the FunnelTemplate keyed on
// (tenant_id, source_template_id). Re-running the importer therefore
// refreshes existing records rather than duplicating them.
func upsertTemplate(t *pkgmodels.FunnelTemplate) error {
	col := db.GetCollection(pkgmodels.FunnelTemplateCollection)
	var existing pkgmodels.FunnelTemplate
	err := col.Find(bson.M{
		"tenant_id":          t.TenantID,
		"source_template_id": t.SourceTemplateID,
	}).One(&existing)
	if err == nil {
		t.Id = existing.Id
		t.PublicId = existing.PublicId
		t.SoftDeletes.CreatedAt = existing.SoftDeletes.CreatedAt
		now := time.Now()
		t.SoftDeletes.UpdatedAt = &now
		return col.UpdateId(existing.Id, bson.M{"$set": t})
	}
	return col.Insert(t)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
