package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/analytics"
	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/egress"
	"github.com/josephalai/sentanyl/pkg/jobs"
	"github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/plans"
	"github.com/josephalai/sentanyl/pkg/scan"
	"github.com/josephalai/sentanyl/pkg/storage"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// SourceKajabi is the only source system this control plane currently speaks.
const SourceKajabi = "kajabi"

// MaxFileBytes bounds one uploaded export file (stored in Mongo).
const MaxFileBytes = 14 << 20 // 14 MiB, under the Mongo doc limit

// AssetStorage is the optional bucket provider for the MIG-005 asset
// pipeline. When unset, asset rows are recorded as errors (explicitly, never
// silently skipped).
var (
	assetStorage storage.StorageProvider
	assetBucket  string
)

// SetAssetStorage wires the GCS provider used for authorized asset copies.
func SetAssetStorage(p storage.StorageProvider, bucket string) {
	assetStorage = p
	assetBucket = bucket
}

// EnsureIndexes creates the migration collections' indexes.
func EnsureIndexes() {
	som := db.GetCollection(models.SourceObjectMapCollection)
	if err := som.EnsureIndex(mgo.Index{
		Key: []string{"tenant_id", "source_system", "source_type", "source_id"}, Unique: true, Background: true,
	}); err != nil {
		log.Printf("migration: source map index: %v", err)
	}
	if err := db.GetCollection(models.MigrationErrorCollection).EnsureIndex(mgo.Index{
		Key: []string{"project_id", "source_type"}, Background: true,
	}); err != nil {
		log.Printf("migration: errors index: %v", err)
	}
	if err := db.GetCollection(models.MigrationFileCollection).EnsureIndex(mgo.Index{
		Key: []string{"project_id", "kind"}, Background: true,
	}); err != nil {
		log.Printf("migration: files index: %v", err)
	}
}

// LoadProject fetches a tenant's project.
func LoadProject(tenantID bson.ObjectId, publicID string) (*models.MigrationProject, error) {
	var p models.MigrationProject
	err := db.GetCollection(models.MigrationProjectCollection).Find(bson.M{
		"tenant_id": tenantID, "public_id": publicID,
	}).One(&p)
	return &p, err
}

func saveProjectState(p *models.MigrationProject, status string, report bson.M, errMsg string) {
	set := bson.M{"status": status, "updated_at": time.Now(), "error": errMsg}
	if report != nil {
		set["report"] = report
	}
	if status == models.MigrationStatusCompleted {
		now := time.Now()
		set["imported_at"] = now
	}
	if status == models.MigrationStatusRolledBack {
		now := time.Now()
		set["rolled_back_at"] = now
	}
	if err := db.GetCollection(models.MigrationProjectCollection).UpdateId(p.Id, bson.M{"$set": set}); err != nil {
		log.Printf("migration: save project %s: %v", p.PublicId, err)
	}
	p.Status = status
}

// StoreFile saves one uploaded export file (replacing a prior file of the
// same kind for the project).
func StoreFile(p *models.MigrationProject, kind, name string, content []byte) error {
	if len(content) > MaxFileBytes {
		return fmt.Errorf("file exceeds %d MiB; split the export", MaxFileBytes>>20)
	}
	_, err := db.GetCollection(models.MigrationFileCollection).Upsert(
		bson.M{"project_id": p.Id, "kind": kind},
		&models.MigrationFile{
			Id: bson.NewObjectId(), TenantID: p.TenantID, ProjectID: p.Id,
			Kind: kind, Name: name, Content: content, SizeBytes: len(content), CreatedAt: time.Now(),
		},
	)
	return err
}

// loadExport parses every stored file for the project.
func loadExport(p *models.MigrationProject) (*Export, []ParseError, error) {
	var files []models.MigrationFile
	if err := db.GetCollection(models.MigrationFileCollection).Find(bson.M{"project_id": p.Id}).All(&files); err != nil {
		return nil, nil, err
	}
	ex := &Export{}
	var errs []ParseError
	for _, f := range files {
		var pe []ParseError
		switch f.Kind {
		case "contacts":
			ex.Contacts, pe = ParseContacts(f.Content)
		case "products":
			ex.Products, pe = ParseProducts(f.Content)
		case "offers":
			ex.Offers, pe = ParseOffers(f.Content)
		case "transactions":
			ex.Transactions, pe = ParseTransactions(f.Content)
		case "grants":
			ex.Grants, pe = ParseGrants(f.Content)
		case "courses":
			ex.Courses, pe = ParseCourses(f.Content)
		case "assets":
			ex.Assets, pe = ParseAssets(f.Content)
		case "subscriptions":
			ex.Subscriptions, pe = ParseSubscriptions(f.Content)
		case "forms":
			ex.Forms, pe = ParseForms(f.Content)
		case "pages":
			ex.Pages, pe = ParsePages(f.Content)
		case "automations":
			ex.Automations, pe = ParseAutomations(f.Content)
		default:
			pe = []ParseError{{Kind: f.Kind, Message: "unknown file kind"}}
		}
		errs = append(errs, pe...)
	}
	DeriveOffers(ex)
	return ex, errs, nil
}

// Validate parses everything and writes the validation report.
func Validate(p *models.MigrationProject) (bson.M, error) {
	ex, errs, err := loadExport(p)
	if err != nil {
		return nil, err
	}
	clearErrors(p, "validate")
	for _, e := range errs {
		recordError(p, "validate", e.Kind, "", e.Row, e.Message)
	}
	report := bson.M{
		"phase":              "validate",
		"counts":             exportCounts(ex),
		"parse_errors":       len(errs),
		"externally_blocked": ExternallyBlocked,
	}
	status := models.MigrationStatusValidated
	if len(ex.Contacts)+len(ex.Transactions)+len(ex.Grants)+len(ex.Products)+len(ex.Offers) == 0 {
		status = models.MigrationStatusDraft
		report["note"] = "no importable rows parsed — upload export files first"
	}
	saveProjectState(p, status, report, "")
	return report, nil
}

// DryRun simulates the import: what would be created vs matched, without
// writing any domain data.
func DryRun(p *models.MigrationProject) (bson.M, error) {
	ex, errs, err := loadExport(p)
	if err != nil {
		return nil, err
	}
	sim := newRun(p, ex, true)
	translations := sim.importAll()
	report := bson.M{
		"phase":                  "dry_run",
		"automation_translation": translations,
		"samples":                previewSamples(ex),
		// MIG-012 coexistence: until the owner-decided cutover, Kajabi stays
		// the source of truth; imported records are drafts/facts only.
		"source_of_truth":    "source platform remains authoritative until you sign off and complete cutover; imported pages/forms/stories are drafts, subscriptions are non-charging until activated",
		"counts":             exportCounts(ex),
		"parse_errors":       len(errs),
		"would_create":       sim.created,
		"would_match":        sim.matched,
		"row_errors":         sim.errors,
		"externally_blocked": ExternallyBlocked,
	}
	saveProjectState(p, models.MigrationStatusDryRun, report, "")
	return report, nil
}

// Execute runs the real import. Idempotent and resumable: every object is
// keyed in the SourceObjectMap, so a rerun (or a resume after a crash) skips
// what already landed and finishes the rest.
func Execute(p *models.MigrationProject) (bson.M, error) {
	return ExecuteWithJob(p, "", "")
}

// ExecuteWithJob is Execute with job-lease context: long phases heartbeat
// the lease every batch, and a retry after a crash resumes from the
// per-phase checkpoint instead of restarting (MIG-010). A deliberate rerun
// of a COMPLETED project resets the checkpoints (row idempotency still
// dedupes); a retry of an interrupted import keeps them.
func ExecuteWithJob(p *models.MigrationProject, jobID bson.ObjectId, worker string) (bson.M, error) {
	resuming := p.Status == models.MigrationStatusImporting
	if !resuming && len(p.CompletedPhases) > 0 {
		p.CompletedPhases = nil
		if err := db.GetCollection(models.MigrationProjectCollection).UpdateId(p.Id, bson.M{
			"$unset": bson.M{"completed_phases": ""},
		}); err != nil {
			log.Printf("migration: reset phase checkpoints: %v", err)
		}
	}
	saveProjectState(p, models.MigrationStatusImporting, nil, "")
	ex, errs, err := loadExport(p)
	if err != nil {
		saveProjectState(p, models.MigrationStatusFailed, nil, err.Error())
		return nil, err
	}
	if !resuming {
		clearErrors(p, "import")
	}
	run := newRun(p, ex, false)
	run.jobID, run.worker = jobID, worker
	translations := run.importAll()
	plans.Invalidate(p.TenantID)

	report := reconcileReport(p, ex)
	report["parse_errors"] = len(errs)
	report["created"] = run.created
	report["matched"] = run.matched
	report["row_errors"] = run.errors
	report["resumed"] = resuming
	if len(translations) > 0 {
		report["automation_translation"] = translations
	}
	saveProjectState(p, models.MigrationStatusCompleted, report, "")
	return report, nil
}

// Rollback deletes every row this project CREATED (matched rows are never
// touched), tenant-scoped, then marks the project rolled back.
func Rollback(p *models.MigrationProject) (bson.M, error) {
	var maps []models.SourceObjectMap
	if err := db.GetCollection(models.SourceObjectMapCollection).Find(bson.M{
		"tenant_id": p.TenantID, "project_id": p.Id, "created": true,
	}).All(&maps); err != nil {
		return nil, err
	}
	removed := map[string]int{}
	for _, m := range maps {
		// An activated (or in-flight) migrated subscription is a live Stripe
		// billing relationship — deleting the record would orphan it. The
		// operator must cancel it in Stripe first; rollback reports it and
		// moves on (MIG-007).
		if m.SourceType == models.SourceTypeSubscription {
			var msub models.MigratedSubscription
			if err := db.GetCollection(models.MigratedSubscriptionCollection).Find(bson.M{
				"_id": m.LocalID, "tenant_id": p.TenantID,
			}).One(&msub); err == nil &&
				(msub.TakeoverState == models.MigratedSubStateActivated ||
					msub.TakeoverState == models.MigratedSubStateRequiresAction) {
				recordError(p, "rollback", m.SourceType, m.SourceID, 0,
					"subscription is "+msub.TakeoverState+" — cancel in Stripe before removing")
				continue
			}
		}
		// A transaction's Purchase also projected PurchaseLog + RevenueFact
		// rows (source=migration) — remove them with it.
		if m.SourceType == models.SourceTypeTransaction {
			var logs []models.PurchaseLog
			_ = db.GetCollection(models.PurchaseLogCollection).Find(bson.M{
				"tenant_id": p.TenantID, "source": "migration", "stripe_charge_id": "kajabi:" + m.SourceID,
			}).All(&logs)
			for _, lg := range logs {
				if info, err := db.GetCollection(models.RevenueFactCollection).RemoveAll(bson.M{
					"tenant_id": p.TenantID, "source_log_id": lg.Id,
				}); err == nil && info != nil {
					removed["revenue_fact"] += info.Removed
				}
				if err := db.GetCollection(models.PurchaseLogCollection).RemoveId(lg.Id); err == nil {
					removed["purchase_log"]++
				}
			}
		}
		// A transaction's Purchase carries dependent rows created with it —
		// purchase items and their migration grants — that must go with it.
		if m.SourceType == models.SourceTypeTransaction {
			var items []models.PurchaseItem
			_ = db.GetCollection(models.PurchaseItemCollection).Find(bson.M{
				"tenant_id": p.TenantID, "purchase_id": m.LocalID,
			}).All(&items)
			for _, item := range items {
				if info, err := db.GetCollection(models.AccessGrantCollection).RemoveAll(bson.M{
					"tenant_id": p.TenantID, "purchase_item_id": item.Id, "source": "migration",
				}); err == nil && info != nil {
					removed["grant"] += info.Removed
				}
			}
			if info, err := db.GetCollection(models.PurchaseItemCollection).RemoveAll(bson.M{
				"tenant_id": p.TenantID, "purchase_id": m.LocalID,
			}); err == nil && info != nil {
				removed["purchase_item"] += info.Removed
			}
		}
		if err := db.GetCollection(m.LocalCollection).Remove(bson.M{"_id": m.LocalID, "tenant_id": p.TenantID}); err != nil {
			// Contacts (users collection) are keyed by tenant_id too; tags use
			// subscriber_id — fall back to a subscriber-scoped delete.
			if err2 := db.GetCollection(m.LocalCollection).Remove(bson.M{"_id": m.LocalID, "subscriber_id": p.TenantID.Hex()}); err2 != nil && err2 != mgo.ErrNotFound && err != mgo.ErrNotFound {
				recordError(p, "rollback", m.SourceType, m.SourceID, 0, "delete failed: "+err.Error())
				continue
			}
		}
		removed[m.SourceType]++
	}
	if _, err := db.GetCollection(models.SourceObjectMapCollection).RemoveAll(bson.M{
		"tenant_id": p.TenantID, "project_id": p.Id,
	}); err != nil {
		log.Printf("migration: clear source map for %s: %v", p.PublicId, err)
	}
	plans.Invalidate(p.TenantID)
	report := bson.M{"phase": "rollback", "removed": removed}
	saveProjectState(p, models.MigrationStatusRolledBack, report, "")
	return report, nil
}

func exportCounts(ex *Export) bson.M {
	return bson.M{
		"contacts": len(ex.Contacts), "products": len(ex.Products), "offers": len(ex.Offers),
		"transactions": len(ex.Transactions), "grants": len(ex.Grants),
		"courses": len(ex.Courses), "assets": len(ex.Assets),
		"subscriptions": len(ex.Subscriptions), "forms": len(ex.Forms), "pages": len(ex.Pages),
		"automations": len(ex.Automations),
	}
}

// previewSamples gives the sign-off reviewer a concrete glimpse of the
// mapped data (first rows per entity), not just counts (MIG-011).
func previewSamples(ex *Export) bson.M {
	take := func(n, max int) int {
		if n < max {
			return n
		}
		return max
	}
	samples := bson.M{}
	if n := take(len(ex.Contacts), 3); n > 0 {
		v := make([]string, 0, n)
		for _, c := range ex.Contacts[:n] {
			v = append(v, c.Email)
		}
		samples["contacts"] = v
	}
	if n := take(len(ex.Offers), 3); n > 0 {
		v := make([]string, 0, n)
		for _, o := range ex.Offers[:n] {
			v = append(v, fmt.Sprintf("%s (%d %s)", o.Title, o.AmountMinor, o.Currency))
		}
		samples["offers"] = v
	}
	if n := take(len(ex.Transactions), 3); n > 0 {
		v := make([]string, 0, n)
		for _, t := range ex.Transactions[:n] {
			v = append(v, fmt.Sprintf("%s → %s (%d %s, %s)", t.Email, t.OfferRef, t.AmountMinor, t.Currency, t.Status))
		}
		samples["transactions"] = v
	}
	if n := take(len(ex.Subscriptions), 3); n > 0 {
		v := make([]string, 0, n)
		for _, sub := range ex.Subscriptions[:n] {
			v = append(v, fmt.Sprintf("%s → %s (%d %s / %s)", sub.Email, sub.OfferRef, sub.AmountMinor, sub.Currency, sub.Interval))
		}
		samples["subscriptions"] = v
	}
	if n := take(len(ex.Forms), 3); n > 0 {
		v := make([]string, 0, n)
		for _, f := range ex.Forms[:n] {
			v = append(v, f.Name)
		}
		samples["forms"] = v
	}
	if n := take(len(ex.Pages), 3); n > 0 {
		v := make([]string, 0, n)
		for _, pg := range ex.Pages[:n] {
			v = append(v, pg.Slug)
		}
		samples["pages"] = v
	}
	return samples
}

// reconcileReport compares source counts against imported map rows.
func reconcileReport(p *models.MigrationProject, ex *Export) bson.M {
	imported := bson.M{}
	for _, st := range []string{
		models.SourceTypeContact, models.SourceTypeTag, models.SourceTypeProduct,
		models.SourceTypeOffer, models.SourceTypeTransaction, models.SourceTypeGrant,
		models.SourceTypeCourse, models.SourceTypeAsset,
		models.SourceTypeSubscription, models.SourceTypeForm, models.SourceTypePage,
		"automation",
	} {
		n, _ := db.GetCollection(models.SourceObjectMapCollection).Find(bson.M{
			"tenant_id": p.TenantID, "source_system": p.SourceSystem, "source_type": st,
		}).Count()
		imported[st] = n
	}
	errCount, _ := db.GetCollection(models.MigrationErrorCollection).Find(bson.M{"project_id": p.Id}).Count()
	return bson.M{
		"phase":              "reconcile",
		"source_counts":      exportCounts(ex),
		"imported_counts":    imported,
		"error_count":        errCount,
		"externally_blocked": ExternallyBlocked,
	}
}

// piiEmail masks email local parts in error messages (MIG-013 log
// redaction): row-level errors stay actionable via source id + row number
// without spraying full addresses through reports and logs.
var piiEmail = regexp.MustCompile(`([A-Za-z0-9._%+-])[A-Za-z0-9._%+-]*(@[A-Za-z0-9.-]+)`)

func redactPII(msg string) string {
	return piiEmail.ReplaceAllString(msg, "$1***$2")
}

func recordError(p *models.MigrationProject, phase, sourceType, sourceID string, row int, msg string) {
	msg = redactPII(msg)
	_ = db.GetCollection(models.MigrationErrorCollection).Insert(&models.MigrationError{
		Id: bson.NewObjectId(), TenantID: p.TenantID, ProjectID: p.Id,
		Phase: phase, SourceType: sourceType, SourceID: sourceID, Row: row,
		Message: msg, CreatedAt: time.Now(),
	})
}

func clearErrors(p *models.MigrationProject, phase string) {
	_, _ = db.GetCollection(models.MigrationErrorCollection).RemoveAll(bson.M{"project_id": p.Id, "phase": phase})
}

// ─── import run ─────────────────────────────────────────────────────────────

type run struct {
	p       *models.MigrationProject
	ex      *Export
	dry     bool
	created map[string]int
	matched map[string]int
	errors  int

	// MIG-010 checkpointing: currentPhase names the running import phase;
	// completedPhases (loaded from the project row) are skipped on resume;
	// jobID/worker let long phases heartbeat their lease; rowsSinceTick
	// drives the batch throttle.
	currentPhase    string
	completedPhases map[string]bool
	jobID           bson.ObjectId
	worker          string
	rowsSinceTick   int

	// resolved source-id → local id caches for cross-references
	productByRef map[string]bson.ObjectId
	offerByRef   map[string]bson.ObjectId
	contactByRef map[string]bson.ObjectId
}

func newRun(p *models.MigrationProject, ex *Export, dry bool) *run {
	r := &run{
		p: p, ex: ex, dry: dry,
		created: map[string]int{}, matched: map[string]int{},
		productByRef:    map[string]bson.ObjectId{},
		offerByRef:      map[string]bson.ObjectId{},
		contactByRef:    map[string]bson.ObjectId{},
		completedPhases: map[string]bool{},
	}
	if !dry {
		for _, ph := range p.CompletedPhases {
			r.completedPhases[ph] = true
		}
	}
	return r
}

// migrationBatchSize is how many processed rows pass between ticks
// (heartbeat + throttle + phase-progress persist).
const migrationBatchSize = 200

// batchThrottle reads MIGRATION_BATCH_THROTTLE_MS once per tick so imports
// can be slowed on shared infrastructure without redeploying.
func batchThrottle() time.Duration {
	if v := os.Getenv("MIGRATION_BATCH_THROTTLE_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return 0
}

// tick runs every processed row; every migrationBatchSize rows it extends
// the job lease and applies the throttle.
func (r *run) tick() {
	r.rowsSinceTick++
	if r.rowsSinceTick < migrationBatchSize {
		return
	}
	r.rowsSinceTick = 0
	if r.dry {
		return
	}
	if r.jobID != "" && r.worker != "" {
		if err := jobs.Heartbeat(r.jobID, r.worker, 2*time.Minute); err != nil {
			log.Printf("migration: heartbeat: %v", err)
		}
	}
	if d := batchThrottle(); d > 0 {
		time.Sleep(d)
	}
}

// phaseDone checkpoints a finished phase on the project row so a resumed
// execute never re-walks it (MIG-010).
func (r *run) phaseDone(name string) {
	if r.dry {
		return
	}
	r.completedPhases[name] = true
	if err := db.GetCollection(models.MigrationProjectCollection).UpdateId(r.p.Id, bson.M{
		"$addToSet": bson.M{"completed_phases": name},
		"$set":      bson.M{"updated_at": time.Now()},
	}); err != nil {
		log.Printf("migration: phase checkpoint %s: %v", name, err)
	}
}

func (r *run) importAll() []AutomationTranslation {
	var translations []AutomationTranslation
	phases := []struct {
		name string
		fn   func()
	}{
		{"products", r.importProducts},
		{"offers", r.importOffers},
		{"contacts", r.importContacts},
		{"courses", r.importCourses},
		{"transactions", r.importTransactions},
		{"grants", r.importGrants},
		{"assets", r.importAssets},
		{"subscriptions", r.importSubscriptions},
		{"forms", r.importForms},
		{"pages", r.importPages},
		{"automations", func() { translations = r.importAutomations() }},
	}
	for _, ph := range phases {
		// The offers/products/contacts phases also build the in-memory
		// cross-reference caches later phases resolve against, so completed
		// reference phases re-walk in cache-only mode via the SourceObjectMap
		// (lookupMap hits mark them matched without writes).
		if r.completedPhases[ph.name] && !phaseBuildsCaches(ph.name) {
			continue
		}
		r.currentPhase = ph.name
		ph.fn()
		r.phaseDone(ph.name)
	}
	return translations
}

// phaseBuildsCaches marks phases whose walk populates resolution caches
// needed by later phases — they rerun even when checkpointed (idempotent:
// every row lookupMap-matches).
func phaseBuildsCaches(name string) bool {
	return name == "products" || name == "offers" || name == "contacts"
}

// lookupMap returns the existing map row's local id, if the source object
// already landed (idempotent rerun/resume).
func (r *run) lookupMap(sourceType, sourceID string) (bson.ObjectId, bool) {
	var m models.SourceObjectMap
	err := db.GetCollection(models.SourceObjectMapCollection).Find(bson.M{
		"tenant_id": r.p.TenantID, "source_system": r.p.SourceSystem,
		"source_type": sourceType, "source_id": sourceID,
	}).One(&m)
	if err != nil {
		return "", false
	}
	return m.LocalID, true
}

func (r *run) record(sourceType, sourceID, localCollection string, localID bson.ObjectId, created bool) {
	r.tick()
	kind := map[bool]string{true: "created", false: "matched"}[created]
	if created {
		r.created[sourceType]++
	} else {
		r.matched[sourceType]++
	}
	if r.dry {
		_ = kind
		return
	}
	err := db.GetCollection(models.SourceObjectMapCollection).Insert(&models.SourceObjectMap{
		Id: bson.NewObjectId(), TenantID: r.p.TenantID, ProjectID: r.p.Id,
		SourceSystem: r.p.SourceSystem, SourceType: sourceType, SourceID: sourceID,
		LocalCollection: localCollection, LocalID: localID, Created: created, CreatedAt: time.Now(),
	})
	if err != nil && !mgo.IsDup(err) {
		log.Printf("migration: source map insert %s/%s: %v", sourceType, sourceID, err)
	}
}

func (r *run) rowError(sourceType, sourceID string, row int, msg string) {
	r.tick()
	r.errors++
	if !r.dry {
		recordError(r.p, "import", sourceType, sourceID, row, msg)
	}
}

// ─── products / offers ──────────────────────────────────────────────────────

func (r *run) importProducts() {
	for _, sp := range r.ex.Products {
		if localID, ok := r.lookupMap(models.SourceTypeProduct, sp.SourceID); ok {
			r.productByRef[strings.ToLower(sp.SourceID)] = localID
			r.matched[models.SourceTypeProduct]++
			continue
		}
		ptype := sp.ProductType
		switch ptype {
		case "course", "download", "newsletter", "service", "community", "coaching":
		default:
			ptype = "download"
		}
		prod := models.NewProduct()
		prod.TenantID = r.p.TenantID
		prod.SubscriberId = r.p.TenantID.Hex()
		prod.Name = sp.Name
		prod.Description = sp.Description
		prod.ProductType = ptype
		prod.Status = "draft" // imported products are reviewed before sale
		if !r.dry {
			if err := db.GetCollection(models.ProductCollection).Insert(prod); err != nil {
				r.rowError(models.SourceTypeProduct, sp.SourceID, sp.Row, "insert: "+err.Error())
				continue
			}
		}
		r.productByRef[strings.ToLower(sp.SourceID)] = prod.Id
		r.record(models.SourceTypeProduct, sp.SourceID, models.ProductCollection, prod.Id, true)
	}
}

// resolveProductRef finds a product by source ref, creating a draft stub when
// the export never described it (offers/assets referencing unknown products).
// An empty ref falls back to the title, so an offer titled "Master Course"
// and a course referencing "Master Course" resolve to the SAME product.
func (r *run) resolveProductRef(ref, forTitle string) bson.ObjectId {
	if ref == "" {
		ref = forTitle
	}
	key := strings.ToLower(ref)
	if id, ok := r.productByRef[key]; ok {
		return id
	}
	sourceID := ref
	if localID, ok := r.lookupMap(models.SourceTypeProduct, sourceID); ok {
		r.productByRef[key] = localID
		return localID
	}
	name := ref
	if name == "" {
		name = forTitle
	}
	prod := models.NewProduct()
	prod.TenantID = r.p.TenantID
	prod.SubscriberId = r.p.TenantID.Hex()
	prod.Name = name + " (imported)"
	prod.ProductType = "download"
	prod.Status = "draft"
	if !r.dry {
		if err := db.GetCollection(models.ProductCollection).Insert(prod); err != nil {
			r.rowError(models.SourceTypeProduct, sourceID, 0, "stub insert: "+err.Error())
			return ""
		}
	}
	r.productByRef[key] = prod.Id
	r.record(models.SourceTypeProduct, sourceID, models.ProductCollection, prod.Id, true)
	return prod.Id
}

func (r *run) importOffers() {
	for _, so := range r.ex.Offers {
		refKeys := []string{strings.ToLower(so.SourceID), strings.ToLower(so.Title)}
		if localID, ok := r.lookupMap(models.SourceTypeOffer, so.SourceID); ok {
			for _, k := range refKeys {
				r.offerByRef[k] = localID
			}
			r.matched[models.SourceTypeOffer]++
			continue
		}
		offer := models.NewOffer(so.Title, r.p.TenantID)
		offer.PricingModel = "one_time"
		if so.AmountMinor == 0 {
			offer.PricingModel = "free"
		}
		offer.Amount = so.AmountMinor
		if so.Currency != "" {
			offer.Currency = so.Currency
		}
		if len(so.ProductIDs) == 0 {
			// Offers must carry ≥1 product (COM-CC-003): resolve a stub.
			if pid := r.resolveProductRef("", so.Title); pid != "" {
				offer.IncludedProducts = []bson.ObjectId{pid}
			}
		}
		for _, ref := range so.ProductIDs {
			if pid := r.resolveProductRef(ref, so.Title); pid != "" {
				offer.IncludedProducts = append(offer.IncludedProducts, pid)
			}
		}
		if !r.dry {
			if err := db.GetCollection(models.OfferCollection).Insert(offer); err != nil {
				r.rowError(models.SourceTypeOffer, so.SourceID, so.Row, "insert: "+err.Error())
				continue
			}
		}
		for _, k := range refKeys {
			r.offerByRef[k] = offer.Id
		}
		r.record(models.SourceTypeOffer, so.SourceID, models.OfferCollection, offer.Id, true)
	}
}

// ─── contacts + tags ────────────────────────────────────────────────────────

// MergeSubscribed decides the post-import subscribed state (consent
// preservation, MIG-004): a local opt-out is never overridden, a source
// unsubscribe is honored, and imports never invent consent a source didn't
// assert. Pure function — unit-tested directly.
func MergeSubscribed(localExists, localSubscribed bool, src SourceContact) bool {
	if localExists && !localSubscribed {
		return false // local opt-out always wins
	}
	if src.SubscribedKnown {
		return src.Subscribed
	}
	if localExists {
		return localSubscribed
	}
	return src.Subscribed // export had no consent column: platform-level opt-in assumed, reported
}

func (r *run) importContacts() {
	tagIDs := map[string]bson.ObjectId{}
	for _, sc := range r.ex.Contacts {
		if localID, ok := r.lookupMap(models.SourceTypeContact, sc.SourceID); ok {
			r.contactByRef[sc.Email] = localID
			r.matched[models.SourceTypeContact]++
			continue
		}
		var existing models.User
		err := db.GetCollection(models.UserCollection).Find(bson.M{
			"subscriber_id": r.p.TenantID.Hex(), "email": sc.Email, "timestamps.deleted_at": nil,
		}).One(&existing)
		localExists := err == nil

		subscribed := MergeSubscribed(localExists, existing.Subscribed, sc)
		if localExists {
			// Merge: fill blanks only, never overwrite curated local data.
			set := bson.M{"subscribed": subscribed}
			if existing.Name.First == "" && sc.FirstName != "" {
				set["name.first"] = sc.FirstName
			}
			if existing.Name.Last == "" && sc.LastName != "" {
				set["name.last"] = sc.LastName
			}
			if existing.Phone == "" && sc.Phone != "" {
				set["phone"] = sc.Phone
			}
			if !r.dry {
				if err := db.GetCollection(models.UserCollection).UpdateId(existing.Id, bson.M{"$set": set}); err != nil {
					r.rowError(models.SourceTypeContact, sc.SourceID, sc.Row, "merge: "+err.Error())
					continue
				}
			}
			r.contactByRef[sc.Email] = existing.Id
			r.record(models.SourceTypeContact, sc.SourceID, models.UserCollection, existing.Id, false)
			r.applyTags(&existing, sc.Tags, tagIDs)
			continue
		}

		u := models.NewUser()
		u.PublicId = utils.GeneratePublicId()
		u.TenantID = r.p.TenantID
		u.SubscriberId = r.p.TenantID.Hex()
		u.Email = models.EmailAddress(sc.Email)
		u.Name.First = sc.FirstName
		u.Name.Last = sc.LastName
		u.Phone = sc.Phone
		u.Subscribed = subscribed
		now := time.Now()
		u.SoftDeletes.CreatedAt = &now
		if !r.dry {
			if err := db.GetCollection(models.UserCollection).Insert(u); err != nil {
				r.rowError(models.SourceTypeContact, sc.SourceID, sc.Row, "insert: "+err.Error())
				continue
			}
		}
		r.contactByRef[sc.Email] = u.Id
		r.record(models.SourceTypeContact, sc.SourceID, models.UserCollection, u.Id, true)
		r.applyTags(u, sc.Tags, tagIDs)
	}
}

func (r *run) applyTags(u *models.User, tags []string, cache map[string]bson.ObjectId) {
	for _, name := range tags {
		key := strings.ToLower(name)
		tagID, ok := cache[key]
		if !ok {
			sourceID := "tag:" + key
			if localID, found := r.lookupMap(models.SourceTypeTag, sourceID); found {
				tagID = localID
			} else {
				var existing models.Tag
				err := db.GetCollection(models.TagCollection).Find(bson.M{
					"subscriber_id": r.p.TenantID.Hex(), "name": name, "timestamps.deleted_at": nil,
				}).One(&existing)
				if err == nil {
					tagID = existing.Id
					r.record(models.SourceTypeTag, sourceID, models.TagCollection, existing.Id, false)
				} else {
					t := models.NewTag()
					t.SubscriberId = r.p.TenantID.Hex()
					t.Name = name
					t.Description = "Imported from " + r.p.SourceSystem
					if !r.dry {
						if err := db.GetCollection(models.TagCollection).Insert(t); err != nil {
							r.rowError(models.SourceTypeTag, sourceID, 0, "insert: "+err.Error())
							continue
						}
					}
					tagID = t.Id
					r.record(models.SourceTypeTag, sourceID, models.TagCollection, t.Id, true)
				}
			}
			cache[key] = tagID
		}
		if r.dry {
			continue
		}
		now := time.Now()
		_ = db.GetCollection(models.UserCollection).Update(
			bson.M{"_id": u.Id, "tags.tag": bson.M{"$ne": tagID}},
			bson.M{"$push": bson.M{"tags": bson.M{"tag": tagID, "when": &now}}},
		)
	}
}

// ─── courses (structure metadata only) ──────────────────────────────────────

func (r *run) importCourses() {
	for i, sc := range r.ex.Courses {
		if _, ok := r.lookupMap(models.SourceTypeCourse, sc.SourceID); ok {
			r.matched[models.SourceTypeCourse]++
			continue
		}
		title := sc.Title
		if title == "" {
			title = sc.ProductRef
		}
		pid := r.resolveProductRef(sc.ProductRef, title)
		if pid == "" {
			r.rowError(models.SourceTypeCourse, sc.SourceID, i+1, "cannot resolve product")
			continue
		}
		var modules []*models.CourseModule
		for order, m := range sc.Modules {
			cm := &models.CourseModule{Slug: slugify(m.Title, order), Title: m.Title, Order: order}
			for lorder, lesson := range m.Lessons {
				cm.Lessons = append(cm.Lessons, &models.CourseLesson{Slug: slugify(lesson, lorder), Title: lesson, Order: lorder, IsDraft: true})
			}
			modules = append(modules, cm)
		}
		if !r.dry {
			if err := db.GetCollection(models.ProductCollection).UpdateId(pid, bson.M{"$set": bson.M{
				"product_type":   "course",
				"course_modules": modules,
			}}); err != nil {
				r.rowError(models.SourceTypeCourse, sc.SourceID, i+1, "structure write: "+err.Error())
				continue
			}
		}
		r.record(models.SourceTypeCourse, sc.SourceID, models.ProductCollection, pid, false)
	}
}

// ─── transactions → imported Purchases + grants ─────────────────────────────

func (r *run) importTransactions() {
	for _, st := range r.ex.Transactions {
		if _, ok := r.lookupMap(models.SourceTypeTransaction, st.SourceID); ok {
			r.matched[models.SourceTypeTransaction]++
			continue
		}
		contactID, ok := r.contactByRef[st.Email]
		if !ok {
			if id, found := r.findLocalContact(st.Email); found {
				contactID = id
			} else {
				r.rowError(models.SourceTypeTransaction, st.SourceID, st.Row, "unknown contact "+st.Email+" (not in contacts.csv or local list)")
				continue
			}
		}
		offerID, ok := r.offerByRef[strings.ToLower(st.OfferRef)]
		if !ok {
			r.rowError(models.SourceTypeTransaction, st.SourceID, st.Row, "unresolved offer "+st.OfferRef)
			continue
		}
		var offer models.Offer
		if err := db.GetCollection(models.OfferCollection).FindId(offerID).One(&offer); err != nil && !r.dry {
			r.rowError(models.SourceTypeTransaction, st.SourceID, st.Row, "offer load: "+err.Error())
			continue
		}

		// Imported financial FACT: never charges, session id carries the
		// source identity so the W2 unique (tenant, session) index dedupes.
		snap := models.OfferSnapshot{OfferID: offerID, Title: offer.Title, PricingModel: offer.PricingModel, Amount: offer.Amount, Currency: offer.Currency}
		purchase := models.NewPurchase(r.p.TenantID, contactID, snap, st.AmountMinor, st.Currency, "kajabi:"+st.SourceID)
		purchase.Status = st.Status // completed | refunded — refunds stay netted per ANA-001
		if !st.OccurredAt.IsZero() {
			occurred := st.OccurredAt
			purchase.SoftDeletes.CreatedAt = &occurred
		}
		if !r.dry {
			if err := db.GetCollection(models.PurchaseCollection).Insert(purchase); err != nil {
				if mgo.IsDup(err) {
					r.matched[models.SourceTypeTransaction]++
					r.record(models.SourceTypeTransaction, st.SourceID, models.PurchaseCollection, purchase.Id, false)
					continue
				}
				r.rowError(models.SourceTypeTransaction, st.SourceID, st.Row, "purchase insert: "+err.Error())
				continue
			}
			for _, productID := range offer.IncludedProducts {
				item := models.NewPurchaseItem(r.p.TenantID, contactID, purchase.Id, offerID, productID, "", offer.Title)
				item.Status = st.Status
				if err := db.GetCollection(models.PurchaseItemCollection).Insert(item); err != nil && !mgo.IsDup(err) {
					r.rowError(models.SourceTypeTransaction, st.SourceID, st.Row, "item insert: "+err.Error())
					continue
				}
				if st.Status != "refunded" {
					grant := models.NewAccessGrant(r.p.TenantID, contactID, productID, item.Id, offerID, "migration")
					if err := db.GetCollection(models.AccessGrantCollection).Insert(grant); err != nil && !mgo.IsDup(err) {
						r.rowError(models.SourceTypeTransaction, st.SourceID, st.Row, "grant insert: "+err.Error())
					}
				}
			}
			// Migrated revenue is VISIBLE revenue: mirror the fact spine
			// (PurchaseLog → RevenueFact) with source=migration so analytics
			// can segment imported vs native income. Refunds net to zero via
			// the paired refund fact (ANA-001 semantics).
			r.writeMigrationRevenue(st, purchase, contactID, offerID, offer)
		}
		r.record(models.SourceTypeTransaction, st.SourceID, models.PurchaseCollection, purchase.Id, true)
	}
}

// writeMigrationRevenue writes the PurchaseLog + revenue facts for one
// imported transaction. Log identity rides StripeChargeId ("kajabi:<id>")
// so rollback can find and remove exactly these rows.
func (r *run) writeMigrationRevenue(st SourceTransaction, purchase *models.Purchase, contactID, offerID bson.ObjectId, offer models.Offer) {
	var productID bson.ObjectId
	if len(offer.IncludedProducts) > 0 {
		productID = offer.IncludedProducts[0]
	}
	pl := &models.PurchaseLog{
		Id: bson.NewObjectId(), PublicId: utils.GeneratePublicId(),
		TenantID: r.p.TenantID, SubscriberId: r.p.TenantID.Hex(),
		UserId: contactID, ProductId: productID, OfferID: offerID,
		Amount: float64(st.AmountMinor) / 100.0, Currency: st.Currency,
		StripeChargeId: "kajabi:" + st.SourceID,
		Status:         st.Status,
		Source:         "migration",
	}
	created := purchase.CreatedAt
	if created == nil {
		now := time.Now()
		created = &now
	}
	pl.SoftDeletes.CreatedAt = created
	if err := db.GetCollection(models.PurchaseLogCollection).Insert(pl); err != nil {
		r.rowError(models.SourceTypeTransaction, st.SourceID, st.Row, "purchase log: "+err.Error())
		return
	}
	analytics.WriteSaleFact(pl)
	if st.Status == "refunded" {
		analytics.WriteRefundFact(pl)
	}
}

func (r *run) importGrants() {
	for _, sg := range r.ex.Grants {
		if _, ok := r.lookupMap(models.SourceTypeGrant, sg.SourceID); ok {
			r.matched[models.SourceTypeGrant]++
			continue
		}
		contactID, ok := r.contactByRef[sg.Email]
		if !ok {
			if id, found := r.findLocalContact(sg.Email); found {
				contactID = id
			} else {
				r.rowError(models.SourceTypeGrant, sg.SourceID, sg.Row, "unknown contact "+sg.Email)
				continue
			}
		}
		var productIDs []bson.ObjectId
		var offerID bson.ObjectId
		if sg.OfferRef != "" {
			oid, ok := r.offerByRef[strings.ToLower(sg.OfferRef)]
			if !ok {
				r.rowError(models.SourceTypeGrant, sg.SourceID, sg.Row, "unresolved offer "+sg.OfferRef)
				continue
			}
			offerID = oid
			var offer models.Offer
			if err := db.GetCollection(models.OfferCollection).FindId(oid).One(&offer); err == nil {
				productIDs = offer.IncludedProducts
			}
		} else if pid := r.resolveProductRef(sg.ProductRef, sg.ProductRef); pid != "" {
			productIDs = []bson.ObjectId{pid}
		}
		if len(productIDs) == 0 {
			if r.dry {
				// Offer/products only exist after a real import — the dry run
				// simulates the grant as creatable.
				r.created[models.SourceTypeGrant]++
				continue
			}
			r.rowError(models.SourceTypeGrant, sg.SourceID, sg.Row, "no products resolve for grant")
			continue
		}
		var firstGrant bson.ObjectId
		if !r.dry {
			for _, pid := range productIDs {
				grant := models.NewAccessGrant(r.p.TenantID, contactID, pid, "", offerID, "migration")
				if err := db.GetCollection(models.AccessGrantCollection).Insert(grant); err != nil && !mgo.IsDup(err) {
					r.rowError(models.SourceTypeGrant, sg.SourceID, sg.Row, "grant insert: "+err.Error())
					continue
				}
				if firstGrant == "" {
					firstGrant = grant.Id
				}
			}
		} else {
			firstGrant = bson.NewObjectId()
		}
		if firstGrant != "" {
			r.record(models.SourceTypeGrant, sg.SourceID, models.AccessGrantCollection, firstGrant, true)
		}
	}
}

func (r *run) findLocalContact(email string) (bson.ObjectId, bool) {
	var u models.User
	err := db.GetCollection(models.UserCollection).Find(bson.M{
		"subscriber_id": r.p.TenantID.Hex(), "email": email, "timestamps.deleted_at": nil,
	}).One(&u)
	if err != nil {
		return "", false
	}
	r.contactByRef[email] = u.Id
	return u.Id, true
}

// ─── assets (MIG-005: authorized copy + checksum) ───────────────────────────

func (r *run) importAssets() {
	for _, sa := range r.ex.Assets {
		if _, ok := r.lookupMap(models.SourceTypeAsset, sa.SourceID); ok {
			r.matched[models.SourceTypeAsset]++
			continue
		}
		if r.dry {
			r.created[models.SourceTypeAsset]++
			continue
		}
		if assetStorage == nil || assetBucket == "" {
			r.rowError(models.SourceTypeAsset, sa.SourceID, sa.Row, "asset storage not configured — asset NOT copied (explicitly skipped, re-run import after configuring GCS)")
			continue
		}
		body, sum, size, err := fetchAsset(sa.URL)
		if err != nil {
			r.rowError(models.SourceTypeAsset, sa.SourceID, sa.Row, "fetch: "+err.Error())
			continue
		}
		name := sa.FileName
		if name == "" {
			name = "asset-" + sum[:12]
		}
		objectPath := fmt.Sprintf("%s/migration/%s/%s_%s", r.p.TenantID.Hex(), r.p.PublicId, sum[:16], name)
		// DEL-018: scan the fetched bytes BEFORE they land in tenant storage.
		if v := scan.GateBytes(r.p.TenantID, assetBucket, objectPath, "migration_asset", body); !v.Allowed {
			r.rowError(models.SourceTypeAsset, sa.SourceID, sa.Row, "quarantined by malware scan: "+v.Reason)
			continue
		}
		if _, err := assetStorage.UploadObject(assetBucket, objectPath, "application/octet-stream", bytes.NewReader(body)); err != nil {
			r.rowError(models.SourceTypeAsset, sa.SourceID, sa.Row, "upload: "+err.Error())
			continue
		}
		asset := models.NewAsset()
		asset.TenantID = r.p.TenantID
		asset.Title = name
		asset.Kind = "download_file"
		asset.Status = "ready"
		asset.FileName = name
		asset.FileSize = size
		asset.S3Key = objectPath
		asset.FileURL = fmt.Sprintf("https://storage.googleapis.com/%s/%s", assetBucket, objectPath)
		asset.Checksum = sum
		if err := db.GetCollection(models.AssetCollection).Insert(asset); err != nil {
			r.rowError(models.SourceTypeAsset, sa.SourceID, sa.Row, "asset insert: "+err.Error())
			continue
		}
		if sa.ProductRef != "" {
			if pid := r.resolveProductRef(sa.ProductRef, name); pid != "" {
				_ = db.GetCollection(models.ProductCollection).Update(
					bson.M{"_id": pid, "tenant_id": r.p.TenantID},
					bson.M{"$push": bson.M{"downloads.asset_ids": asset.Id}},
				)
			}
		}
		r.record(models.SourceTypeAsset, sa.SourceID, models.AssetCollection, asset.Id, true)
	}
}

// fetchAsset downloads one source asset over the SSRF-safe egress boundary
// (WH-002) and returns its bytes, sha256, and size. The egress client caps
// bodies at 8 MiB; a body AT the cap is treated as truncated and rejected —
// a corrupted copy is worse than a reported failure.
func fetchAsset(rawURL string) ([]byte, string, int64, error) {
	body, resp, err := egress.Get(context.Background(), rawURL)
	if err != nil {
		return nil, "", 0, err
	}
	if resp.StatusCode >= 300 {
		return nil, "", 0, fmt.Errorf("source returned %d", resp.StatusCode)
	}
	if len(body) >= 8<<20 {
		return nil, "", 0, fmt.Errorf("asset exceeds the 8 MiB egress cap — host it directly and re-attach")
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), int64(len(body)), nil
}

// slugify builds a stable slug from a title (course structure import).
func slugify(title string, order int) string {
	s := strings.ToLower(strings.TrimSpace(title))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = fmt.Sprintf("item-%d", order+1)
	}
	return out
}
