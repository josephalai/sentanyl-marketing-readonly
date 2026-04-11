package main

import (
	"log"
	"os"

	"github.com/gin-gonic/gin"

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

	// Public funnel routes (page serving, events).
	api := r.Group("/api")
	routes.RegisterFunnelRoutes(api)
	routes.RegisterEmailRoutes(api)
	routes.RegisterOutboundWebhookRoutes(api)

	// Protected tenant routes (require JWT).
	tenantAPI := r.Group("/api/tenant")
	tenantAPI.Use(auth.RequireTenantAuth())
	routes.RegisterEcommerceRoutes(tenantAPI)

	// Customer-facing routes (require customer JWT).
	customerAPI := r.Group("/api/customer")
	customerAPI.Use(auth.RequireCustomerAuth())
	routes.RegisterCustomerLibraryRoutes(customerAPI)

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
