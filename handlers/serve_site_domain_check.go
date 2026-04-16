package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/josephalai/sentanyl/marketing-service/internal/site"
)

// RegisterInternalDomainCheck registers the internal domain validation
// endpoint used by Caddy's on-demand TLS to verify that a hostname is
// a legitimate, verified website domain before issuing a certificate.
// This endpoint is NOT exposed publicly — only reachable from the
// internal Docker network.
func RegisterInternalDomainCheck(internalAPI *gin.RouterGroup) {
	internalAPI.GET("/domain/check", handleInternalDomainCheck)
}

// handleInternalDomainCheck returns 200 if the queried domain is an
// attached, verified website domain. Caddy's on_demand_tls ask
// directive calls this endpoint.
//
// Query params:
//
//	domain — the hostname Caddy wants to issue a certificate for.
func handleInternalDomainCheck(c *gin.Context) {
	domain := c.Query("domain")
	if domain == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "domain query parameter required"})
		return
	}

	// Verify this domain is attached to a published site.
	if err := site.VerifyDomainForTLS(domain); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain not authorized"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "domain": domain})
}
