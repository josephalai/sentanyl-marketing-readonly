package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/analytics"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
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
	// ANA-009 drill-through: the raw facts behind any summary number.
	rp.GET("/analytics/facts", handleAnalyticsFacts)
	// ANA-012: tenant-facing AI usage rollup over the ai_executions ledger.
	rp.GET("/analytics/ai-usage", handleAIUsage)
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
		"window":  gin.H{"from": c.Query("from"), "to": c.Query("to")},
		"sources": analytics.RevenueBySource(tenantID, factWindow(c)),
	})
}

// factWindow parses optional from/to unix-seconds query params.
func factWindow(c *gin.Context) analytics.FactWindow {
	w := analytics.FactWindow{}
	if v, err := strconv.ParseInt(c.Query("from"), 10, 64); err == nil && v > 0 {
		w.From = time.Unix(v, 0)
	}
	if v, err := strconv.ParseInt(c.Query("to"), 10, 64); err == nil && v > 0 {
		w.To = time.Unix(v, 0)
	}
	return w
}

// handleAnalyticsFacts is the ANA-009 drill-through: paged raw revenue-fact
// rows behind the summaries, with completeness metadata (total matches +
// facts-vs-ledger reconciliation) so a drilled number is verifiable.
func handleAnalyticsFacts(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	q := analytics.FactsQuery{
		TenantID: tenantID,
		Kind:     c.Query("kind"),
		Source:   c.Query("source"),
		Window:   factWindow(c),
	}
	if v, err := strconv.Atoi(c.Query("limit")); err == nil {
		q.Limit = v
	}
	if v, err := strconv.Atoi(c.Query("skip")); err == nil {
		q.Skip = v
	}
	rows, total, err := analytics.Facts(q)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load facts"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"facts":       rows,
		"total":       total,
		"returned":    len(rows),
		"completeness": analytics.Reconcile(tenantID),
	})
}

// handleAIUsage rolls up the ai_executions ledger by surface/outcome/day.
func handleAIUsage(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	w := factWindow(c)
	match := bson.M{"tenant_id": tenantID}
	rng := bson.M{}
	if !w.From.IsZero() {
		rng["$gte"] = w.From
	}
	if !w.To.IsZero() {
		rng["$lt"] = w.To
	}
	if len(rng) > 0 {
		match["created_at"] = rng
	}
	rows := []bson.M{}
	if err := db.GetCollection(pkgmodels.AIExecutionCollection).Pipe([]bson.M{
		{"$match": match},
		{"$group": bson.M{
			"_id": bson.M{
				"surface": "$surface",
				"outcome": "$outcome",
				"model":   "$model",
				"day":     bson.M{"$dateToString": bson.M{"format": "%Y-%m-%d", "date": "$created_at"}},
			},
			"count": bson.M{"$sum": 1},
		}},
		{"$sort": bson.M{"_id.day": -1}},
		{"$limit": 1000},
	}).All(&rows); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to aggregate ai usage"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"usage": rows})
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
