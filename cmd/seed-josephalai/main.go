// Command seed-josephalai idempotently provisions the josephalai.net proof
// data for the BYO coded-website channel: the workshop/masterclass catalog,
// Custom Imaginal Scenes service tiers, coaching packages + program, the
// Private Dispatch newsletter, the coaching application form, coach
// availability, and a coded_website FrontendChannel with a public key.
//
// Prices and titles come from the 2026-07-10 josephalai.com crawl inventory
// (docs/audits/2026-07-10-josephalai-com-crawl-inventory.md).
//
// Usage:
//
//	go run ./marketing-service/cmd/seed-josephalai -tenant-id <hex>
//	go run ./marketing-service/cmd/seed-josephalai -tenant-name "Joseph Alai" \
//	   -domain josephalai-net.localhost -out proof-sites/josephalai-net/assets/config.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

type offerSpec struct {
	Name        string
	Description string
	ProductType string
	AmountCents int64
	Badge       string
	ConfigKey   string // key in the emitted config.json offers map
}

var workshops = []offerSpec{
	{"Manifest Faster with Joseph: Advanced Fundamentals of Manifesting", "Advanced fundamentals workshop.", pkgmodels.ProductTypeCourse, 10000, "owns:manifest-faster", "manifest_faster"},
	{"Unlock the Power of Manifestation: Step-by-Step Entering The End", "Step-by-step Entering The End workshop.", pkgmodels.ProductTypeCourse, 10000, "owns:entering-the-end", "entering_the_end"},
	{"Reality Creation Masterclass - Crafting Your New Reality with Joseph Alai", "Reality creation masterclass.", pkgmodels.ProductTypeCourse, 10000, "owns:reality-creation", "reality_creation"},
	{"Manifesting a Specific Person - Workshop 1", "Specific person workshop 1.", pkgmodels.ProductTypeCourse, 10000, "owns:sp-workshop-1", "sp_workshop_1"},
	{"Mental Diet and Precision Manifesting Workshop", "Mental diet and precision manifesting.", pkgmodels.ProductTypeCourse, 10000, "owns:mental-diet", "mental_diet"},
	{"Joseph's Scientific Approach to Manifesting Workshop", "Scientific approach to manifesting.", pkgmodels.ProductTypeCourse, 10000, "owns:scientific-approach", "scientific_approach"},
	{"Turning Manifesting Into A Lifestyle Workshop", "Manifesting as a lifestyle.", pkgmodels.ProductTypeCourse, 10000, "owns:lifestyle", "lifestyle"},
	{"Manifesting Basics - Neville Goddard Techniques", "Neville Goddard basics.", pkgmodels.ProductTypeCourse, 10000, "owns:basics", "basics"},
}

var sceneTiers = []offerSpec{
	{"Single Custom Imaginal Scene", "One bespoke imaginal scene, crafted to your specification.", pkgmodels.ProductTypeService, 20000, "owns:scene-single", "scene_single"},
	{"Harmonic Method", "The Harmonic Method custom scene package.", pkgmodels.ProductTypeService, 55000, "owns:scene-harmonic", "scene_harmonic"},
	{"Five-Star Experience", "The Five-Star custom scene experience.", pkgmodels.ProductTypeService, 88000, "owns:scene-five-star", "scene_five_star"},
	{"God-Tier Bundle", "The complete God-Tier custom scene bundle.", pkgmodels.ProductTypeService, 170000, "owns:scene-god-tier", "scene_god_tier"},
}

var coachingPackages = []offerSpec{
	{"Mini Momentum Builder (3 x 30min)", "Three 30-minute coaching sessions.", pkgmodels.ProductTypeCoaching, 90000, "owns:coach-3x30", "coach_3x30"},
	{"Transformation Lite (5 x 30min)", "Five 30-minute coaching sessions.", pkgmodels.ProductTypeCoaching, 147000, "owns:coach-5x30", "coach_5x30"},
	{"Holistic Accelerator (10 x 30min)", "Ten 30-minute coaching sessions.", pkgmodels.ProductTypeCoaching, 294000, "owns:coach-10x30", "coach_10x30"},
	{"Breakthrough Accelerator (3 x 45min)", "Three 45-minute coaching sessions.", pkgmodels.ProductTypeCoaching, 126000, "owns:coach-3x45", "coach_3x45"},
	{"Transformation Immersion (5 x 45min)", "Five 45-minute coaching sessions.", pkgmodels.ProductTypeCoaching, 207000, "owns:coach-5x45", "coach_5x45"},
	{"Manifestation Powerhouse (10 x 45min)", "Ten 45-minute coaching sessions.", pkgmodels.ProductTypeCoaching, 408000, "owns:coach-10x45", "coach_10x45"},
	{"Manifestation Deep Dive (45min)", "A single 45-minute deep-dive session.", pkgmodels.ProductTypeCoaching, 45000, "owns:coach-45-single", "coach_45_single"},
}

func main() {
	var (
		host         string
		port         string
		dbName       string
		tenantHex    string
		tenantName   string
		createTenant bool
		domain       string
		portalDomain string
		outPath      string
		apiBase      string
	)
	flag.StringVar(&host, "mongo-host", envOr("MONGO_HOST", "localhost"), "Mongo host")
	flag.StringVar(&port, "mongo-port", envOr("MONGO_PORT", "27017"), "Mongo port")
	flag.StringVar(&dbName, "mongo-db", envOr("MONGO_DB", "sentanyl_db"), "Mongo database name")
	flag.StringVar(&tenantHex, "tenant-id", "", "Tenant ObjectId hex (preferred)")
	flag.StringVar(&tenantName, "tenant-name", "", "Tenant business name lookup (alternative to -tenant-id)")
	flag.BoolVar(&createTenant, "create-tenant", false, "Create the -tenant-name tenant if it doesn't exist (also marks -domain as a verified tenant domain)")
	flag.StringVar(&domain, "domain", "josephalai.net", "Channel domain (use josephalai-net.localhost for e2e)")
	flag.StringVar(&portalDomain, "portal-domain", "", "Sentanyl-served tenant domain for portal handoff (e.g. portal.josephalai.net); marked verified, sets channel portal/success/cancel URLs")
	flag.StringVar(&outPath, "out", "", "Optional path to write a config.json for the static proof site")
	flag.StringVar(&apiBase, "api-base", "", "API base URL to embed in config.json (empty = same origin)")
	flag.Parse()

	db.MongoHost = host
	db.MongoPort = port
	db.MongoDB = dbName
	db.MongoDefaultCollectionName = "users"
	db.UsingLocalMongo = true
	db.InitMongoConnection()

	tenantID := resolveTenant(tenantHex, tenantName, createTenant)
	log.Printf("seed-josephalai: tenant %s domain %s", tenantID.Hex(), domain)

	// Operator-seeded domains skip DNS verification: mark the channel domain
	// as a verified tenant domain so Host-based public resolution works.
	if createTenant {
		upsertVerifiedDomain(tenantID, domain)
	}

	cfg := map[string]interface{}{
		"domain":     domain,
		"api_base":   apiBase,
		"offers":     map[string]string{},
		"products":   map[string]string{},
		"offers_hex": map[string]string{}, // internal ids for the e2e simulate-purchase shim
	}
	offerIds := cfg["offers"].(map[string]string)
	productIds := cfg["products"].(map[string]string)
	offerHexIds := cfg["offers_hex"].(map[string]string)

	seedCatalog := func(specs []offerSpec) {
		for _, spec := range specs {
			p := upsertProduct(tenantID, spec)
			o := upsertOffer(tenantID, spec, p)
			offerIds[spec.ConfigKey] = o.PublicId
			productIds[spec.ConfigKey] = p.PublicId
			offerHexIds[spec.ConfigKey] = o.Id.Hex()
		}
	}
	seedCatalog(workshops)
	seedCatalog(sceneTiers)
	seedCatalog(coachingPackages)

	// Coaching program (native scheduling, 45-minute sessions) — the booking
	// surface behind /coaching and /client-packages. Attach it to the single
	// deep-dive product so a purchase grants program access.
	program := upsertCoachingProgram(tenantID)
	cfg["coaching_program_public_id"] = program.PublicId
	upsertAvailability(tenantID)

	// Newsletter product.
	newsletter := upsertNewsletter(tenantID)
	cfg["newsletter_product_public_id"] = newsletter.PublicId

	// Coaching application form (fields from the crawled /coaching page).
	form := upsertApplicationForm(tenantID)
	cfg["coaching_form_public_id"] = form.PublicId

	// Manifesting readiness quiz — the /research page's embedded assessment
	// (data-sentanyl-quiz), doubling as a lead magnet.
	quiz := upsertReadinessQuiz(tenantID)
	cfg["quiz_public_id"] = quiz.PublicId

	// Portal handoff domain — a tenant domain served by the Sentanyl edge
	// (the proxy CNAME model). Buyers land here after Stripe checkout.
	if portalDomain != "" {
		upsertVerifiedDomain(tenantID, portalDomain)
	}

	// Coded-website frontend channel.
	channel := upsertChannel(tenantID, domain, portalDomain)
	cfg["public_key"] = channel.PublicKey
	cfg["channel_public_id"] = channel.PublicId
	if channel.PortalBaseURL != "" {
		cfg["portal_base_url"] = channel.PortalBaseURL
	}

	blob, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Println(string(blob))
	if outPath != "" {
		if err := os.WriteFile(outPath, blob, 0o644); err != nil {
			log.Fatalf("seed-josephalai: write %s: %v", outPath, err)
		}
		log.Printf("seed-josephalai: wrote %s", outPath)
	}
}

func resolveTenant(tenantHex, tenantName string, createTenant bool) bson.ObjectId {
	if tenantHex != "" {
		if !bson.IsObjectIdHex(tenantHex) {
			log.Fatalf("seed-josephalai: invalid -tenant-id %q", tenantHex)
		}
		var t pkgmodels.Tenant
		if err := db.GetCollection(pkgmodels.TenantCollection).FindId(bson.ObjectIdHex(tenantHex)).One(&t); err != nil {
			log.Fatalf("seed-josephalai: tenant %s not found: %v", tenantHex, err)
		}
		return t.Id
	}
	if tenantName != "" {
		var t pkgmodels.Tenant
		err := db.GetCollection(pkgmodels.TenantCollection).Find(bson.M{
			"business_name":         tenantName,
			"timestamps.deleted_at": nil,
		}).One(&t)
		if err == nil {
			return t.Id
		}
		if !createTenant {
			log.Fatalf("seed-josephalai: tenant %q not found (pass -create-tenant to create): %v", tenantName, err)
		}
		nt := pkgmodels.NewTenant(tenantName)
		nt.SubscriptionStatus = "active"
		if err := db.GetCollection(pkgmodels.TenantCollection).Insert(nt); err != nil {
			log.Fatalf("seed-josephalai: insert tenant %q: %v", tenantName, err)
		}
		log.Printf("  + tenant %q %s", tenantName, nt.Id.Hex())
		return nt.Id
	}
	log.Fatal("seed-josephalai: -tenant-id or -tenant-name is required")
	return ""
}

func upsertVerifiedDomain(tenantID bson.ObjectId, hostname string) {
	col := db.GetCollection(pkgmodels.DomainCollection)
	var existing pkgmodels.TenantDomain
	if err := col.Find(bson.M{
		"hostname":              hostname,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&existing); err == nil {
		if !existing.IsVerified {
			_ = col.UpdateId(existing.Id, bson.M{"$set": bson.M{"is_verified": true}})
			log.Printf("  ~ tenant domain %s marked verified", hostname)
		}
		return
	}
	d := pkgmodels.NewTenantDomain(hostname, tenantID)
	d.IsVerified = true
	if err := col.Insert(d); err != nil {
		log.Fatalf("seed-josephalai: insert tenant domain %s: %v", hostname, err)
	}
	log.Printf("  + tenant domain %s (verified)", hostname)
}

func upsertProduct(tenantID bson.ObjectId, spec offerSpec) *pkgmodels.Product {
	col := db.GetCollection(pkgmodels.ProductCollection)
	var existing pkgmodels.Product
	if err := col.Find(bson.M{
		"tenant_id":             tenantID,
		"name":                  spec.Name,
		"timestamps.deleted_at": nil,
	}).One(&existing); err == nil {
		return &existing
	}
	now := time.Now()
	p := pkgmodels.NewProduct()
	p.TenantID = tenantID
	p.Name = spec.Name
	p.Description = spec.Description
	p.ProductType = spec.ProductType
	p.Status = pkgmodels.ProductStatusActive
	p.Price = float64(spec.AmountCents) / 100
	p.Currency = "usd"
	p.SoftDeletes.CreatedAt = &now
	if err := col.Insert(p); err != nil {
		log.Fatalf("seed-josephalai: insert product %q: %v", spec.Name, err)
	}
	log.Printf("  + product %-60s %s", spec.Name, p.PublicId)
	return p
}

func upsertOffer(tenantID bson.ObjectId, spec offerSpec, product *pkgmodels.Product) *pkgmodels.Offer {
	col := db.GetCollection(pkgmodels.OfferCollection)
	var existing pkgmodels.Offer
	if err := col.Find(bson.M{
		"tenant_id":             tenantID,
		"title":                 spec.Name,
		"timestamps.deleted_at": nil,
	}).One(&existing); err == nil {
		return &existing
	}
	now := time.Now()
	o := pkgmodels.NewOffer(spec.Name, tenantID)
	o.PricingModel = "one_time"
	o.Amount = spec.AmountCents
	// Library hydration walks contact.badges → offers.granted_badges →
	// included_products; an offer without a badge leaves the buyer's
	// library empty even after a successful purchase.
	o.GrantedBadges = []string{spec.Badge}
	o.IncludedProducts = []bson.ObjectId{product.Id}
	o.SoftDeletes.CreatedAt = &now
	if err := col.Insert(o); err != nil {
		log.Fatalf("seed-josephalai: insert offer %q: %v", spec.Name, err)
	}
	log.Printf("  + offer   %-60s %s ($%.2f)", spec.Name, o.PublicId, float64(o.Amount)/100)
	return o
}

func upsertCoachingProgram(tenantID bson.ObjectId) *pkgmodels.Product {
	const name = "Manifestation Coaching with Joseph Alai"
	col := db.GetCollection(pkgmodels.ProductCollection)
	var existing pkgmodels.Product
	if err := col.Find(bson.M{
		"tenant_id":             tenantID,
		"name":                  name,
		"timestamps.deleted_at": nil,
	}).One(&existing); err == nil {
		return &existing
	}
	now := time.Now()
	p := pkgmodels.NewProduct()
	p.TenantID = tenantID
	p.Name = name
	p.Description = "One-on-one manifestation coaching program."
	p.ProductType = pkgmodels.ProductTypeCoaching
	p.Status = pkgmodels.ProductStatusActive
	p.Coaching = &pkgmodels.CoachingConfig{
		Modality:   pkgmodels.CoachingModalityOneOnOne,
		CoachName:  "Joseph Alai",
		CoachEmail: "joseph@josephalai.net",
		SessionTemplates: []*pkgmodels.SessionTemplate{
			{Id: bson.NewObjectId(), Order: 1, Title: "Coaching Session", DurationMin: 45},
		},
		Scheduling: pkgmodels.SchedulingConfig{
			Provider:           pkgmodels.CoachingSchedulerNative,
			SessionDurationMin: 45,
			LocationKind:       pkgmodels.CoachingLocationLiveKit,
		},
	}
	p.SoftDeletes.CreatedAt = &now
	if err := col.Insert(p); err != nil {
		log.Fatalf("seed-josephalai: insert coaching program: %v", err)
	}
	log.Printf("  + coaching program %s", p.PublicId)
	return p
}

func upsertAvailability(tenantID bson.ObjectId) {
	col := db.GetCollection(pkgmodels.CoachAvailabilityCollection)
	n, _ := col.Find(bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}).Count()
	if n > 0 {
		return
	}
	now := time.Now()
	avail := pkgmodels.CoachAvailability{
		Id:       bson.NewObjectId(),
		TenantID: tenantID,
		Name:     "Default",
		Active:   true,
		Timezone: "America/New_York",
		Windows: []*pkgmodels.AvailabilityRule{
			{Weekday: 1, StartTime: "09:00", EndTime: "17:00"},
			{Weekday: 2, StartTime: "09:00", EndTime: "17:00"},
			{Weekday: 3, StartTime: "09:00", EndTime: "17:00"},
			{Weekday: 4, StartTime: "09:00", EndTime: "17:00"},
			{Weekday: 5, StartTime: "09:00", EndTime: "17:00"},
		},
		MaxFutureDays: 30,
	}
	avail.SoftDeletes.CreatedAt = &now
	if err := col.Insert(avail); err != nil {
		log.Fatalf("seed-josephalai: insert availability: %v", err)
	}
	log.Printf("  + coach availability (Mon-Fri 9-5 ET)")
}

func upsertReadinessQuiz(tenantID bson.ObjectId) *pkgmodels.LMSQuiz {
	const title = "Manifesting Readiness Assessment"
	col := db.GetCollection(pkgmodels.LMSQuizCollection)
	var existing pkgmodels.LMSQuiz
	if err := col.Find(bson.M{
		"tenant_id":             tenantID,
		"title":                 title,
		"timestamps.deleted_at": nil,
	}).One(&existing); err == nil {
		return &existing
	}
	now := time.Now()
	q := pkgmodels.NewLMSQuiz()
	q.TenantID = tenantID
	q.SubscriberId = tenantID.Hex()
	q.Slug = "manifesting-readiness"
	q.Title = title
	q.PassThreshold = 67
	q.Questions = []*pkgmodels.LMSQuizQuestion{
		{
			Slug: "state", Type: "multiple_choice", Order: 1,
			Title:         "In the SATS method, what matters most about the scene you construct?",
			Options:       []string{"Its length", "That it implies the wish fulfilled", "Its visual detail", "Repeating it aloud"},
			CorrectAnswer: 1,
		},
		{
			Slug: "denominator", Type: "multiple_choice", Order: 2,
			Title:         "Common Denominator Analysis isolates what?",
			Options:       []string{"Your fastest technique", "The shared element across your past successes", "Your ideal bedtime", "The best affirmation wording"},
			CorrectAnswer: 1,
		},
		{
			Slug: "persistence", Type: "multiple_choice", Order: 3,
			Title:         "When results lag, the systematic response is to…",
			Options:       []string{"Switch goals", "Abandon the scene", "Return to the state nightly until it feels natural", "Add more techniques"},
			CorrectAnswer: 2,
		},
	}
	q.SoftDeletes.CreatedAt = &now
	if err := col.Insert(q); err != nil {
		log.Fatalf("seed-josephalai: insert quiz: %v", err)
	}
	log.Printf("  + quiz %s", q.PublicId)
	return q
}

func upsertNewsletter(tenantID bson.ObjectId) *pkgmodels.Product {
	const name = "Private Dispatch: Systematic Manifesting"
	col := db.GetCollection(pkgmodels.ProductCollection)
	var existing pkgmodels.Product
	if err := col.Find(bson.M{
		"tenant_id":             tenantID,
		"name":                  name,
		"timestamps.deleted_at": nil,
	}).One(&existing); err == nil {
		return &existing
	}
	now := time.Now()
	p := pkgmodels.NewProduct()
	p.TenantID = tenantID
	p.Name = name
	p.Description = "Systematic manifesting insights, delivered privately."
	p.ProductType = pkgmodels.ProductTypeNewsletter
	p.Status = pkgmodels.ProductStatusActive
	p.Newsletter = &pkgmodels.NewsletterConfig{
		Tagline:             "Systematic manifesting, in your inbox.",
		PublishCadence:      "weekly",
		DoubleOptInEnabled:  true,
		DefaultPostAudience: pkgmodels.NewsletterAudienceAll,
	}
	p.SoftDeletes.CreatedAt = &now
	if err := col.Insert(p); err != nil {
		log.Fatalf("seed-josephalai: insert newsletter: %v", err)
	}
	log.Printf("  + newsletter %s", p.PublicId)
	return p
}

func upsertApplicationForm(tenantID bson.ObjectId) *pkgmodels.PageForm {
	const name = "Coaching Application"
	col := db.GetCollection(pkgmodels.PageFormCollection)
	var existing pkgmodels.PageForm
	if err := col.Find(bson.M{
		"tenant_id":             tenantID,
		"name":                  name,
		"timestamps.deleted_at": nil,
	}).One(&existing); err == nil {
		return &existing
	}

	badgePub := upsertBadge(tenantID, "Coaching Applicant")

	now := time.Now()
	form := pkgmodels.PageForm{
		Id:       bson.NewObjectId(),
		PublicId: utils.GeneratePublicId(),
		TenantID: tenantID,
		Name:     name,
		FormType: "application",
		Fields: []*pkgmodels.FormField{
			{FieldName: "name", FieldType: "text", Required: true},
			{FieldName: "email", FieldType: "email", Required: true},
			{FieldName: "readiness", FieldType: "text"},
			{FieldName: "support_type", FieldType: "text"},
			{FieldName: "prior_teachers", FieldType: "text"},
			{FieldName: "interests", FieldType: "text"},
			{FieldName: "comments", FieldType: "textarea"},
		},
		OnSubmit: &pkgmodels.FormOnSubmit{
			UpsertContact:   true,
			WriteAttributes: true,
			AssignBadgeIds:  []string{badgePub},
		},
	}
	form.SoftDeletes.CreatedAt = &now
	if err := col.Insert(form); err != nil {
		log.Fatalf("seed-josephalai: insert form: %v", err)
	}
	log.Printf("  + form %q %s", name, form.PublicId)
	return &form
}

func upsertBadge(tenantID bson.ObjectId, name string) string {
	col := db.GetCollection(pkgmodels.BadgeCollection)
	var existing pkgmodels.Badge
	if err := col.Find(bson.M{"tenant_id": tenantID, "name": name}).One(&existing); err == nil {
		return existing.PublicId
	}
	b := pkgmodels.NewBadge()
	b.TenantID = tenantID
	b.Name = name
	b.Description = name
	if err := col.Insert(b); err != nil {
		log.Fatalf("seed-josephalai: insert badge %q: %v", name, err)
	}
	return b.PublicId
}

func upsertChannel(tenantID bson.ObjectId, domain, portalDomain string) *pkgmodels.FrontendChannel {
	col := db.GetCollection(pkgmodels.FrontendChannelCollection)
	var existing pkgmodels.FrontendChannel
	if err := col.Find(bson.M{
		"tenant_id":             tenantID,
		"domain":                domain,
		"timestamps.deleted_at": nil,
	}).One(&existing); err == nil {
		return &existing
	}
	now := time.Now()
	ch := pkgmodels.NewFrontendChannel("josephalai.net", pkgmodels.FrontendChannelTypeCodedWebsite, tenantID)
	ch.Status = pkgmodels.FrontendChannelStatusActive
	ch.Domain = domain
	if portalDomain != "" {
		ch.PortalBaseURL = "https://" + portalDomain + "/portal"
		ch.DefaultSuccessURL = "https://" + portalDomain + "/portal/welcome?session_id={CHECKOUT_SESSION_ID}"
		ch.DefaultCancelURL = "https://" + domain + "/"
	}
	key, err := auth.GeneratePublicKey()
	if err != nil {
		log.Fatalf("seed-josephalai: generate public key: %v", err)
	}
	ch.PublicKey = key
	ch.SoftDeletes.CreatedAt = &now
	if err := col.Insert(ch); err != nil {
		log.Fatalf("seed-josephalai: insert channel: %v", err)
	}
	log.Printf("  + channel %s domain=%s key=%s", ch.PublicId, domain, key)
	return ch
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
