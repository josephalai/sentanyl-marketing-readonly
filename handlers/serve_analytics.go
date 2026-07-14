package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/josephalai/sentanyl/marketing-service/internal/analytics"
	"github.com/josephalai/sentanyl/pkg/auth"
)

// RegisterAnalyticsRoutes mounts the canonical analytics surface
// (ANA-004..008): the metric registry, facts-based attribution reporting,
// projection rebuild, and facts↔source reconciliation.
func RegisterAnalyticsRoutes(tenantAPI *gin.RouterGroup) {
	// ANA-010: analytics reads are owner/admin-only (financial data); rebuild
	// stays owner-only (destructive reprojection).
	rp := tenantAPI.Group("", auth.RequirePermission(auth.PermReportsView))
	rp.GET("/analytics/metrics", handleAnalyticsMetrics)
	rp.GET("/analytics/revenue/by-source", handleRevenueBySource)
	rp.GET("/analytics/reconcile", handleAnalyticsReconcile)
	tenantAPI.POST("/analytics/rebuild-facts", auth.RequireOwner(), handleRebuildFacts)
}

// handleAnalyticsMetrics returns the canonical metric definitions — what
// each reported number means, from where, in what unit and timezone.
func handleAnalyticsMetrics(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"metrics":                  analytics.Registry,
		"attribution_window_days":  analytics.AttributionWindowDays(),
	})
}

// handleRevenueBySource reports net revenue grouped by the attributed
// acquisition touch (funnel/form/direct), excluding synthetic traffic.
func handleRevenueBySource(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"metric":  "revenue.by_source",
		"sources": analytics.RevenueBySource(tenantID),
	})
}

// handleAnalyticsReconcile compares facts totals against the purchase-log
// source aggregation per currency.
func handleAnalyticsReconcile(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	c.JSON(http.StatusOK, analytics.Reconcile(tenantID))
}

// handleRebuildFacts reprojects the tenant's facts from purchase_logs.
// Idempotent; owner-gated (it walks the whole ledger).
func handleRebuildFacts(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	sales, refunds := analytics.RebuildFacts(tenantID)
	c.JSON(http.StatusOK, gin.H{"sales_written": sales, "refunds_written": refunds})
}
