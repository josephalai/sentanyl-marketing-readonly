package main

import (
	"log"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/josephalai/sentanyl/marketing-service/handlers"
	"github.com/josephalai/sentanyl/marketing-service/internal/ai"
	imapsync "github.com/josephalai/sentanyl/marketing-service/internal/imap"
	"github.com/josephalai/sentanyl/marketing-service/internal/scheduler"
	"github.com/josephalai/sentanyl/marketing-service/internal/site"
	"github.com/josephalai/sentanyl/marketing-service/routes"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/config"
	"github.com/josephalai/sentanyl/pkg/db"
	httputil "github.com/josephalai/sentanyl/pkg/http"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/render"
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

	// Ensure MongoDB indexes for Inbox Closer AI queues and tenant records.
	routes.EnsureInboxIndexes()

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
	scheduler.Start()

	// Inbox Closer — register the IMAP inbound handler and start polling
	// all connected IMAP accounts every 2 minutes.
	routes.RegisterIMAPHandler()
	imapsync.StartSyncLoop(2 * time.Minute)
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
		defer p.Close()
	}

	// Set up Gin router.
	r := gin.Default()
	r.Use(httputil.CORSMiddleware())

	// Public marketing routes (page serving, events, subscriber-scoped product reads).
	api := r.Group("/api/marketing")
	routes.RegisterFunnelRoutes(api)
	routes.RegisterEmailRoutes(api)
	routes.RegisterOutboundWebhookRoutes(api)

	// Protected tenant routes (require JWT).
	// Scoped under /api/marketing/tenant/* to avoid collisions with public routes above.
	// Caddy routes all /api/marketing/* to this service, so both are reachable.
	tenantAPI := r.Group("/api/marketing/tenant")
	tenantAPI.Use(auth.RequireTenantAuth())
	routes.RegisterEcommerceRoutes(tenantAPI)
	routes.RegisterInboxCloserRoutes(tenantAPI)

	// Legacy /api/tenant/* paths — frontend pages call these directly (pre-refactor paths).
	// Caddy now routes /api/tenant/products*, /api/tenant/offers*, etc. to this service.
	legacyTenantAPI := r.Group("/api/tenant")
	legacyTenantAPI.Use(auth.RequireTenantAuth())
	routes.RegisterEcommerceRoutes(legacyTenantAPI)
	routes.RegisterLegacyTenantFunnelRoutes(legacyTenantAPI)
	routes.RegisterNewsletterTenantRoutes(legacyTenantAPI)
	routes.RegisterInboxCloserRoutes(legacyTenantAPI)

	// Legacy /api/funnel/* path — FunnelTemplatesPage calls /api/funnel/template.
	legacyFunnelAPI := r.Group("/api/funnel")
	legacyFunnelAPI.Use(auth.RequireTenantAuth())
	routes.RegisterLegacyFunnelTemplateRoutes(legacyFunnelAPI)

	// Customer-facing routes (require customer JWT).
	customerAPI := r.Group("/api/customer")
	customerAPI.Use(auth.RequireCustomerAuth())
	routes.RegisterCustomerLibraryRoutes(customerAPI)
	routes.RegisterCustomerLibraryDetailRoutes(customerAPI)
	routes.RegisterNewsletterCustomerRoutes(customerAPI)

	// Forms management (tenant-scoped CRUD for PageForm entities).
	handlers.RegisterFormsRoutes(legacyTenantAPI)

	// Email AI generation and editing.
	handlers.RegisterEmailAIRoutes(legacyTenantAPI)

	// Newsletter authoring AI (post generation + editing).
	handlers.RegisterNewsletterAIRoutes(legacyTenantAPI)

	// Funnel AI — template-based generation.
	handlers.RegisterFunnelAIRoutes(legacyTenantAPI)

	// Site themes and starter kits.
	handlers.RegisterSiteThemeRoutes(legacyTenantAPI)

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

	// Public form submission and checkout routes (no auth — for published websites).
	handlers.RegisterPublicFormRoutes(api)

	// Public newsletter subscribe / confirm / unsubscribe routes (no auth).
	routes.RegisterNewsletterPublicRoutes(api)

	// Stripe webhook receiver (platform-wide endpoint, dispatched per-tenant via ?tenant_id=).
	handlers.RegisterStripeWebhookRoute(api)

	// Post-checkout lookup for the /portal/welcome landing page. Public —
	// resolves tenant from the request Host header.
	handlers.RegisterCheckoutLookupRoute(r.Group("/api/customer"))

	// Internal routes (no auth — internal network only).
	internal := r.Group("/internal")
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
