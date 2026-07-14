package routes

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/audit"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/scan"
)

// DEL-018: the centralized malware-scan gate for every upload path that
// finalizes via attach in this service (digital downloads, service
// deliverables, customer intake files). Migration assets gate inside the
// import engine on the in-memory bytes.

// scanGateAttach streams the just-uploaded object to the scanner and blocks
// the attach unless the verdict allows it. Returns false after writing the
// HTTP response when blocked.
func scanGateAttach(c *gin.Context, tenantID bson.ObjectId, objectPath, surface string) bool {
	rc, err := downloadStorage.ReadObject(downloadBucket, objectPath)
	if err != nil {
		log.Printf("scan: read %s for scanning: %v", objectPath, err)
		if scan.Required() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "uploaded object could not be scanned — try again"})
			return false
		}
		return true
	}
	defer rc.Close()
	v := scan.Gate(tenantID, downloadBucket, objectPath, surface, rc, 0)
	if !v.Allowed {
		e := audit.FromContext(c)
		e.Action, e.Outcome, e.Reason = "upload.quarantine", "denied", v.Reason
		e.TargetType, e.TargetID = "object", objectPath
		e.Meta = bson.M{"signature": v.Signature, "surface": surface}
		audit.Record(e)
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "file failed malware scanning and was quarantined"})
		return false
	}
	return true
}

// scanGateServe blocks signed-URL issuance for objects that are not clean.
// Returns false after writing the response when blocked.
func scanGateServe(c *gin.Context, tenantID bson.ObjectId, objectPath string) bool {
	if blocked, reason := scan.Blocked(tenantID, objectPath); blocked {
		c.JSON(http.StatusForbidden, gin.H{"error": reason})
		return false
	}
	return true
}

// RegisterScanOpsRoutes exposes quarantine visibility + audited rescan and
// release to owners (PermDataDestroy — platform-operator authority). Mounted
// under /migration so Caddy's existing /api/tenant/migration* route carries
// it to this service.
func RegisterScanOpsRoutes(rg *gin.RouterGroup) {
	ops := rg.Group("/migration/quarantine", auth.RequirePermission(auth.PermDataDestroy))
	ops.GET("", handleQuarantineList)
	ops.POST("/rescan", handleQuarantineRescan)
	ops.POST("/release", handleQuarantineRelease)
}

func handleQuarantineList(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	rows, err := scan.ListQuarantined(tenantID, 200)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list quarantine"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"quarantined": rows, "scanner_healthy": scan.Healthy() == nil})
}

func handleQuarantineRescan(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	var req struct {
		ObjectPath string `json:"object_path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "object_path required"})
		return
	}
	rc, err := downloadStorage.ReadObject(downloadBucket, req.ObjectPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "object unreadable"})
		return
	}
	defer rc.Close()
	v := scan.Gate(tenantID, downloadBucket, req.ObjectPath, "rescan", rc, 0)
	e := audit.FromContext(c)
	e.Action, e.Outcome = "quarantine.rescan", v.Status
	e.TargetType, e.TargetID = "object", req.ObjectPath
	audit.Record(e)
	c.JSON(http.StatusOK, gin.H{"status": v.Status, "signature": v.Signature})
}

func handleQuarantineRelease(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	var req struct {
		ObjectPath string `json:"object_path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "object_path required"})
		return
	}
	actor := c.GetString(auth.ContextAccountUserID)
	if err := scan.Release(tenantID, req.ObjectPath, actor); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "object is not quarantined"})
		return
	}
	e := audit.FromContext(c)
	e.Action, e.Outcome = "quarantine.release", "success"
	e.TargetType, e.TargetID = "object", req.ObjectPath
	audit.Record(e)
	c.JSON(http.StatusOK, gin.H{"status": "released"})
}
