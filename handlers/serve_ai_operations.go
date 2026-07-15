package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/aigov"
	"github.com/josephalai/sentanyl/pkg/audit"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

const aiOperationContextKey = "ai_operation_context"

// GovernAI admits a provider call before its handler runs and settles the
// reservation after the response. The public operation id is emitted before
// generation starts so another authenticated session can cancel it.
func GovernAI(surface string, outputTokens int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := auth.GetTenantObjectID(c)
		if tenantID == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		var inputChars int64
		if c.Request.Body != nil {
			body, err := io.ReadAll(io.LimitReader(c.Request.Body, 8<<20))
			if err != nil {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
				return
			}
			inputChars = int64(len(body))
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
		}
		now := time.Now().UTC()
		op, err := aigov.Begin(tenantID, surface, aigov.Estimate{InputCharacters: inputChars, OutputTokens: outputTokens}, now)
		if err != nil {
			code := "ai_admission_failed"
			switch {
			case errors.Is(err, aigov.ErrConcurrencyLimit):
				code = "ai_concurrency_limit"
			case errors.Is(err, aigov.ErrCostBudgetExceeded):
				code = "ai_cost_budget_exceeded"
			}
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": err.Error(), "code": code})
			return
		}
		ctx, cancel := aigov.Context(c.Request.Context(), op)
		defer cancel()
		c.Set(aiOperationContextKey, ctx)
		c.Header("X-AI-Operation-Id", op.PublicID)
		c.Next()

		if c.Writer.Status() >= 200 && c.Writer.Status() < 300 && ctx.Err() == nil {
			_ = aigov.Complete(op, aigov.Usage{}, time.Now().UTC())
		} else {
			cause := ctx.Err()
			if cause == nil {
				cause = errors.New(http.StatusText(c.Writer.Status()))
			}
			_ = aigov.Fail(op, cause, time.Now().UTC())
		}
	}
}

func aiRequestContext(c *gin.Context) context.Context {
	if value, ok := c.Get(aiOperationContextKey); ok {
		if ctx, ok := value.(context.Context); ok {
			return ctx
		}
	}
	return c.Request.Context()
}

// RegisterAIOperationRoutes exposes tenant-scoped cost/concurrency state and
// cancellation. The surrounding group already enforces tenant auth.
func RegisterAIOperationRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.GET("/ai/operations", auth.RequirePermission(auth.PermReportsView), handleListAIOperations)
	tenantAPI.GET("/ai/budget", auth.RequirePermission(auth.PermReportsView), handleGetAIBudget)
	tenantAPI.POST("/ai/operations/:publicId/cancel", auth.RequirePermission(auth.PermContentManage), handleCancelAIOperation)
}

func handleListAIOperations(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	var rows []pkgmodels.AIOperation
	if err := db.GetCollection(pkgmodels.AIOperationCollection).Find(bson.M{"tenant_id": tenantID}).Sort("-started_at").Limit(100).All(&rows); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list AI operations"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"operations": rows})
}

func handleGetAIBudget(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	day := time.Now().UTC().Format("2006-01-02")
	var budget pkgmodels.AIBudgetDay
	err := db.GetCollection(pkgmodels.AIBudgetDayCollection).FindId(tenantID.Hex() + ":" + day).One(&budget)
	if err != nil && err != mgo.ErrNotFound {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read AI budget"})
		return
	}
	if err == mgo.ErrNotFound {
		budget.Day = day
	}
	c.JSON(http.StatusOK, gin.H{
		"day": budget.Day, "reserved_micros": budget.ReservedMicros, "spent_micros": budget.SpentMicros,
		"active": budget.Active, "daily_limit_micros": aigov.DailyCostLimitMicros(), "max_concurrent": aigov.MaxConcurrent(),
	})
}

func handleCancelAIOperation(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	publicID := c.Param("publicId")
	err := aigov.RequestCancel(tenantID, publicID, time.Now().UTC())
	if err == mgo.ErrNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "active AI operation not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to cancel AI operation"})
		return
	}
	e := audit.FromContext(c)
	e.Action, e.TargetType, e.TargetID, e.Outcome = "ai.operation.cancel", "ai_operation", publicID, "success"
	audit.Record(e)
	c.JSON(http.StatusAccepted, gin.H{"status": pkgmodels.AIOperationCancelRequested})
}
