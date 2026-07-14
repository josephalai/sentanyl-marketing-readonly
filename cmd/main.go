package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/josephalai/sentanyl/marketing-service/handlers"
	"github.com/josephalai/sentanyl/marketing-service/internal/ai"
	"github.com/josephalai/sentanyl/marketing-service/internal/analytics"
	"github.com/josephalai/sentanyl/marketing-service/internal/channel"
	imapsync "github.com/josephalai/sentanyl/marketing-service/internal/imap"
	"github.com/josephalai/sentanyl/marketing-service/internal/migration"
	"github.com/josephalai/sentanyl/marketing-service/internal/scheduler"
	"github.com/josephalai/sentanyl/marketing-service/internal/site"
	"github.com/josephalai/sentanyl/marketing-service/internal/webhooks"
	"github.com/josephalai/sentanyl/marketing-service/routes"
	"github.com/josephalai/sentanyl/pkg/aigov"
	"github.com/josephalai/sentanyl/pkg/audit"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/jobs"
	"github.com/josephalai/sentanyl/pkg/config"
	"github.com/josephalai/sentanyl/pkg/badges"
	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/entitlements"
	httputil "github.com/josephalai/sentanyl/pkg/http"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/render"
	"github.com/josephalai/sentanyl/pkg/scan"
	"github.com/josephalai/sentanyl/pkg/storage"
)

func main() {
	log.Println("marketing-service: starting up")

	// Load config from .env if present.
	if _, err := os.Stat(".env"); err == nil {
		configVals := config.LoadConfigFile(config.ConfigFile)
		config.MapConfigValues(configVals)
	}

	// Determine port (default 8082 for marketing-service).
	port := envOrDefault("MARKETING_SERVICE_PORT", "8082")

	// Connect to MongoDB.
	db.MongoHost = envOrDefault("MONGO_HOST", "localhost")
	db.MongoPort = envOrDefault("MONGO_PORT", "27017")
	db.MongoDB = envOrDefault("MONGO_DB", "sentanyl")
	db.MongoDefaultCollectionName = "funnels"
	db.UsingLocalMongo = true
	db.InitMongoConnection()

	// Ensure MongoDB indexes for website builder collections.
	site.EnsureIndexes()

	// Ensure MongoDB indexes for ecommerce collections (coupon dedupe, etc).
	routes.EnsureEcommerceIndexes()

	// Ensure MongoDB indexes for download upload intents (DEL-010).
	routes.EnsureDownloadIndexes()

	// Ensure FUL-001/002 service-fulfillment invariants.
	routes.EnsureServiceFulfillmentIndexes()

	// Durable job kernel: indexes, handlers, and a background worker for
	// outbound webhook delivery (WH-003) and future durable workloads.
	jobs.EnsureIndexes()
	// Inbox-agent machine principals are minted from this service (MCP-001);
	// AI executions are ledgered from the inbox/site AI paths (AI-001).
	auth.EnsurePrincipalIndexes()
	aigov.EnsureIndexes()
	// Migration control plane (MIG-001..005): source-map idempotency indexes
	// + the durable execute job.
	migration.EnsureIndexes()
	scan.EnsureIndexes()
	routes.RegisterMigrationJobs()
	// Revenue facts projection (ANA-005): unique (tenant, log, kind).
	analytics.EnsureIndexes()
	webhooks.RegisterHandlers()
	routes.RegisterStoryStartJob()
	go jobs.RunWorker(context.Background(), jobs.WorkerConfig{Name: "marketing-" + auth.ServiceName("worker")})

	// Ensure MongoDB indexes for frontend channels (coded websites, etc).
	channel.EnsureIndexes()

	// Ensure MongoDB indexes for Inbox Closer AI queues and tenant records.
	routes.EnsureInboxIndexes()
	routes.EnsureTodoIndexes()

	// Bootstrap the {{ai}} handlebar resolver. Constructs a process-wide
	// singleton wired to the configured SiteAIProvider; broadcast send,
	// public page render, and the customer post API all consume it via
	// ai.Resolver(). Nil-safe end-to-end — when no LLM provider is wired
	// the resolver still emits "[ai unavailable]" placeholders so render
	// paths never panic.
	provider, _ := ai.GetConfiguredProvider()
	textProvider := ai.NewResolverAdapter(provider)
	ai.SetResolver(render.NewAIResolver(textProvider, pkgmodels.NewsletterDefaultAITTLSeconds))

	// Newsletter scheduler — auto-publishes scheduled posts at their time
	// and dispatches drip-mode posts per-subscriber. In-process goroutine
	// matching the coaching reminder worker pattern.
	scheduler.DripEmailRenderer = routes.RenderAndAuthorizeDripEmail
	scheduler.Start()

	// Inbox Closer — register the IMAP inbound handler and start polling
	// all connected IMAP accounts every 2 minutes.
	routes.RegisterIMAPHandler()
	imapsync.StartSyncLoop(2 * time.Minute)

	// DEL-016: flip overdue active access grants to expired (read-side
	// authorization already honors expires_at; this projects the lifecycle).
	entitlements.StartExpirySweep(10 * time.Minute)

	// ID-012/DEL-004: badge provenance invariant + the trusted consumer for
	// server-side video badge qualifications.
	badges.EnsureIndexes()
	badges.RegisterMediaQualifiedConsumer()

	// COM-EM-001: immutable delivery ProviderEvent invariant.
	routes.EnsureDeliveryEventIndexes()

	// COM-EM-005: durable campaign dispatch (recipient uniqueness + job).
	routes.EnsureCampaignIndexes()
	httputil.EnsureIdempotencyIndexes()
	routes.RegisterCampaignDispatchJob()
	routes.StartTimerApprovalLoop()

	// Initialize the GCS storage provider used by digital download deliveries
	// and the service product resource uploads. If init fails (no ADC in dev),
	// downloads routes will return 503 with a clear message rather than
	// silently fail.
	gcsBucket := envOrDefault("GCS_BUCKET", "sendhero-videos")
	gcsProject := envOrDefault("GCP_PROJECT_ID", "sendhero")
	if p, err := storage.NewGCSProvider(gcsProject); err != nil {
		log.Printf("marketing-service: GCS init failed (downloads will return 503): %v", err)
	} else {
		routes.SetDownloadsStorage(p, gcsBucket)
		migration.SetAssetStorage(p, gcsBucket)
		defer p.Close()
	}

	// Set up Gin router.
	r := gin.Default()

	// Tenant mailbox OAuth callback (COM-EM-003) — public; auth is the
	// HMAC-signed state minted by the authorize-url endpoint.
	routes.RegisterInboxOAuthPublicRoutes(r)
	audit.Init("marketing-service")
	r.Use(httputil.CORSMiddleware())
	r.Use(audit.Middleware())

	r.GET("/health", httputil.HealthHandler("marketing-service"))

	// Public marketing routes (page serving, events).
	api := r.Group("/api/marketing")
	routes.RegisterFunnelRoutes(api)
	routes.RegisterEmailRoutes(api)
	// Contact-level one-click unsubscribe (campaign/story/A-B emails). Not
	// rate-limited: mailbox providers batch one-click POSTs, and a dropped
	// unsubscribe is a compliance failure.
	routes.RegisterUnsubscribeRoutes(api)

	// Public campaign click tracker — recipients have no JWT, so this lives
	// outside the tenant-auth group. Engine-level register since it's not
	// confined to /api/marketing.
	routes.RegisterCampaignTrackingRoutes(r)
	// Unified per-email open pixel + click redirect (story/campaign/newsletter).
	routes.RegisterEmailTrackingRoutes(r)

	// Tenant send API — accepts per-tenant X-API-Key OR tenant JWT. Sibling
	// group on the same prefix as tenantAPI below (gin allows both); external
	// apps authenticate here with their tenant API key.
	tenantSendAPI := r.Group("/api/marketing/tenant")
	// Throttle per credential (API key, else JWT tenant, else IP) AFTER auth so
	// the JWT tenant id is resolved. A leaked API key is bounded to one bucket
	// no matter how many hosts replay it.
	sendKeyFn := func(c *gin.Context) string {
		if k := c.GetHeader("X-API-Key"); k != "" {
			return "key:" + k
		}
		if t := auth.GetTenantID(c); t != "" {
			return "tenant:" + t
		}
		return ""
	}
	tenantSendAPI.Use(auth.RequireTenantAuthOrAPIKey(), auth.RequirePlatformSubscription(),
		httputil.RateLimitByKey(sendKeyFn, 120, 60))
	routes.RegisterTenantEmailRoutes(tenantSendAPI)

	// Protected tenant routes (require JWT).
	// Scoped under /api/marketing/tenant/* to avoid collisions with public routes above.
	// Caddy routes all /api/marketing/* to this service, so both are reachable.
	tenantAPI := r.Group("/api/marketing/tenant")
	tenantAPI.Use(auth.RequireTenantAuth(), auth.RequirePlatformSubscription())
	routes.RegisterEcommerceRoutes(tenantAPI)
	routes.RegisterInboxCloserRoutes(tenantAPI)
	routes.RegisterTodoRoutes(tenantAPI)
	// Outbound webhook CRUD — was on the public group scoped by a trusted
	// subscriber_id query param; now JWT-scoped and tenant-authed.
	routes.RegisterOutboundWebhookRoutes(tenantAPI)

	// A/B broadcast testing — tenant-authed, mounted at /api/ab to match the
	// admin abService contract (Caddy routes /api/ab/* here).
	abAPI := r.Group("/api/ab")
	abAPI.Use(auth.RequireTenantAuth(), auth.RequirePlatformSubscription())
	routes.RegisterABTestingRoutes(abAPI)

	// Legacy /api/tenant/* paths — frontend pages call these directly (pre-refactor paths).
	// Caddy now routes /api/tenant/products*, /api/tenant/offers*, etc. to this service.
	legacyTenantAPI := r.Group("/api/tenant")
	legacyTenantAPI.Use(auth.RequireTenantAuth(), auth.RequirePlatformSubscription())
	routes.RegisterEcommerceRoutes(legacyTenantAPI)
	// Kajabi migration control plane (MIG-001..005) — owner-gated inside.
	routes.RegisterMigrationRoutes(legacyTenantAPI)
	// DEL-018 quarantine visibility + audited rescan/release (owner-gated inside).
	routes.RegisterScanOpsRoutes(legacyTenantAPI)
	routes.RegisterLegacyTenantFunnelRoutes(legacyTenantAPI)
	routes.RegisterNewsletterTenantRoutes(legacyTenantAPI)
	routes.RegisterInboxCloserRoutes(legacyTenantAPI)
	routes.RegisterTodoRoutes(legacyTenantAPI)

	// Legacy /api/funnel/* path — FunnelTemplatesPage calls /api/funnel/template.
	legacyFunnelAPI := r.Group("/api/funnel")
	legacyFunnelAPI.Use(auth.RequireTenantAuth(), auth.RequirePlatformSubscription())
	routes.RegisterLegacyFunnelTemplateRoutes(legacyFunnelAPI)

	// Customer-facing routes (require customer JWT).
	customerAPI := r.Group("/api/customer")
	customerAPI.Use(auth.RequireCustomerAuth())
	routes.RegisterCustomerLibraryRoutes(customerAPI)
	routes.RegisterCustomerLibraryDetailRoutes(customerAPI)
	routes.RegisterNewsletterCustomerRoutes(customerAPI)

	// Forms management (tenant-scoped CRUD for PageForm entities).
	handlers.RegisterFormsRoutes(legacyTenantAPI)

	// Phase 5 — admin Purchases + Revenue surfaces.
	handlers.RegisterPurchasesRoutes(legacyTenantAPI)
	handlers.RegisterRevenueRoutes(legacyTenantAPI)
	// Canonical analytics: metric registry, facts, attribution (ANA-004..008).
	handlers.RegisterAnalyticsRoutes(legacyTenantAPI)

	// Email AI generation and editing.
	handlers.RegisterEmailAIRoutes(legacyTenantAPI)

	// Campaigns — one-off email sends with badge-defined audience.
	routes.RegisterCampaignRoutes(legacyTenantAPI)
	handlers.RegisterCampaignAIRoutes(legacyTenantAPI)

	// Newsletter authoring AI (post generation + editing).
	handlers.RegisterNewsletterAIRoutes(legacyTenantAPI)

	// Funnel AI — template-based generation.
	handlers.RegisterFunnelAIRoutes(legacyTenantAPI)

	// Site themes and starter kits.
	handlers.RegisterSiteThemeRoutes(legacyTenantAPI)

	// Frontend channels (coded websites) tenant CRUD.
	handlers.RegisterFrontendChannelRoutes(legacyTenantAPI)

	// Website builder tenant routes (require JWT).
	// Scoped under /api/tenant/sites* — Caddy routes these to marketing-service.
	handlers.RegisterSiteRoutes(legacyTenantAPI)
	handlers.RegisterSitePageRoutes(legacyTenantAPI)
	handlers.RegisterSiteAIRoutes(legacyTenantAPI)
	handlers.RegisterSitePublishRoutes(legacyTenantAPI)
	handlers.RegisterSiteResourceRoutes(legacyTenantAPI)
	handlers.RegisterSiteDuplicateRoutes(legacyTenantAPI)

	// Public website delivery route (no auth — serves published HTML snapshots).
	// Caddy routes designated website hosts to this endpoint.
	handlers.RegisterPublicSiteRoutes(api)

	// Browser-viewable site routes — /view/sites/:publicId[/slug]
	// No auth, public_id only, serves raw HTML directly in the browser.
	handlers.RegisterSiteViewRoutes(r)

	// Public static assets used by published pages (sentanyl-video.js
	// runtime player). Mounted on the root engine, not the /api group,
	// so the URL `/static/sentanyl-video.js` is stable across tenant hosts.
	handlers.RegisterPublicSiteAssetRoutes(r)

	// Public form submission and checkout routes (no auth — for published
	// websites). Same per-IP throttle as /api/public/* below: unauth surface
	// that triggers writes and emails (double-opt-in, autoresponders).
	publicFormGroup := r.Group("/api/marketing")
	publicFormGroup.Use(httputil.RateLimit(60, 30))
	// API-003: honor an Idempotency-Key on public writes so a retried form
	// submit / checkout start cannot create a duplicate.
	publicFormGroup.Use(httputil.Idempotency())
	handlers.RegisterPublicFormRoutes(publicFormGroup)

	// Frontend-channel public integration surface (no auth — for coded
	// websites and other frontend channels). Stable contract under
	// /api/public/*; reuses the same form/checkout/newsletter internals as
	// the builder routes above. Rate-limited per IP (public/unauth surface:
	// checkout-session spam, form/newsletter abuse).
	publicGroup := r.Group("/api/public")
	publicGroup.Use(httputil.RateLimit(60, 30))
	publicGroup.Use(httputil.Idempotency()) // API-003
	handlers.RegisterPublicChannelRoutes(publicGroup)

	// Public newsletter subscribe / confirm / unsubscribe routes (no auth).
	// Throttled per IP — subscribe fires a double-opt-in email per request.
	routes.RegisterNewsletterPublicRoutes(publicFormGroup)

	// Stripe webhook receiver (platform-wide endpoint, dispatched per-tenant via ?tenant_id=).
	handlers.RegisterStripeWebhookRoute(api)

	// Post-checkout lookup for the /portal/welcome landing page. Public —
	// resolves tenant from the request Host header.
	handlers.RegisterCheckoutLookupRoute(r.Group("/api/customer"))

	// Internal routes — signed service identity required (API-001); network
	// position is not identity.
	internal := r.Group("/internal", auth.RequireServiceAuth())
	routes.RegisterInternalRoutes(internal)

	// E2E test-only routes, gated by SENTANYL_E2E_MODE=1 inside each handler.
	// Mounted on both /internal/* (in-cluster) and /api/marketing/test/* so the
	// puppeteer harness can reach them through Caddy from the host.
	handlers.RegisterE2ETestRoutes(internal)
	e2ePublic := r.Group("/api/marketing/test")
	handlers.RegisterE2ETestRoutes(e2ePublic)
	routes.RegisterInternalE2ETestRoutes(e2ePublic)

	// Internal domain validation for Caddy on-demand TLS.
	// Caddy calls this to verify a hostname before issuing a certificate.
	handlers.RegisterInternalDomainCheck(internal)

	log.Printf("marketing-service: listening on :%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("marketing-service: failed to start: %v", err)
	}
}

func envOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
