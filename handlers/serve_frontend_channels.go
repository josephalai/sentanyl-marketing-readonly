package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/josephalai/sentanyl/marketing-service/internal/channel"
	"github.com/josephalai/sentanyl/pkg/auth"
)

// RegisterFrontendChannelRoutes wires tenant-scoped CRUD for frontend
// channels (coded websites etc.) under /api/tenant/frontend-channels.
func RegisterFrontendChannelRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.GET("/frontend-channels", handleListFrontendChannels)
	tenantAPI.POST("/frontend-channels", handleCreateFrontendChannel)
	tenantAPI.GET("/frontend-channels/:channelId", handleGetFrontendChannel)
	tenantAPI.PUT("/frontend-channels/:channelId", handleUpdateFrontendChannel)
	tenantAPI.DELETE("/frontend-channels/:channelId", handleDeleteFrontendChannel)
	tenantAPI.POST("/frontend-channels/:channelId/rotate-key", handleRotateFrontendChannelKey)
}

func handleListFrontendChannels(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	channels, err := channel.ServiceListChannels(tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list channels"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"channels": channels})
}

func handleCreateFrontendChannel(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req channel.ChannelUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	ch, err := channel.ServiceCreateChannel(req, tenantID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"status": "ok", "channel": ch})
}

func handleGetFrontendChannel(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	ch, err := channel.ServiceGetChannel(tenantID, c.Param("channelId"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"channel": ch})
}

func handleUpdateFrontendChannel(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req channel.ChannelUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	ch, err := channel.ServiceUpdateChannel(tenantID, c.Param("channelId"), req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "channel": ch})
}

func handleDeleteFrontendChannel(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	if err := channel.ServiceDeleteChannel(tenantID, c.Param("channelId")); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleRotateFrontendChannelKey(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	ch, err := channel.ServiceRotateChannelKey(tenantID, c.Param("channelId"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "channel": ch})
}
