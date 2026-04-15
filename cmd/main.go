package main

import (
	"log"
	"os"

	"github.com/gin-gonic/gin"

	"github.com/josephalai/sentanyl/marketing-service/handlers"
	"github.com/josephalai/sentanyl/marketing-service/routes"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/config"
	"github.com/josephalai/sentanyl/pkg/db"
	httputil "github.com/josephalai/sentanyl/pkg/http"
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

	// Legacy /api/tenant/* paths — frontend pages call these directly (pre-refactor paths).
	// Caddy now routes /api/tenant/products*, /api/tenant/offers*, etc. to this service.
	legacyTenantAPI := r.Group("/api/tenant")
	legacyTenantAPI.Use(auth.RequireTenantAuth())
	routes.RegisterEcommerceRoutes(legacyTenantAPI)
	routes.RegisterLegacyTenantFunnelRoutes(legacyTenantAPI)

	// Legacy /api/funnel/* path — FunnelTemplatesPage calls /api/funnel/template.
	legacyFunnelAPI := r.Group("/api/funnel")
	legacyFunnelAPI.Use(auth.RequireTenantAuth())
	routes.RegisterLegacyFunnelTemplateRoutes(legacyFunnelAPI)

	// Customer-facing routes (require customer JWT).
	customerAPI := r.Group("/api/customer")
	customerAPI.Use(auth.RequireCustomerAuth())
	routes.RegisterCustomerLibraryRoutes(customerAPI)

	// Website builder tenant routes (require JWT).
	// Scoped under /api/tenant/sites* — Caddy routes these to marketing-service.
	handlers.RegisterSiteRoutes(legacyTenantAPI)
	handlers.RegisterSitePageRoutes(legacyTenantAPI)
	handlers.RegisterSiteAIRoutes(legacyTenantAPI)
	handlers.RegisterSitePublishRoutes(legacyTenantAPI)

	// Public website delivery route (no auth — serves published HTML snapshots).
	// Caddy routes designated website hosts to this endpoint.
	handlers.RegisterPublicSiteRoutes(api)

	// Internal routes (no auth — internal network only).
	internal := r.Group("/internal")
	routes.RegisterInternalRoutes(internal)

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
