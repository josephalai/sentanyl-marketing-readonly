package routes

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/migration"
	"github.com/josephalai/sentanyl/pkg/audit"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/jobs"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// Migration control plane API (MIG-001..005). Owner-gated: importing a whole
// business is owner authority, machine actors are denied by RequireOwner.
func RegisterMigrationRoutes(rg *gin.RouterGroup) {
	m := rg.Group("/migration", auth.RequireOwner())
	{
		m.POST("/projects", handleMigrationCreate)
		m.GET("/projects", handleMigrationList)
		m.GET("/projects/:projectId", handleMigrationGet)
		m.POST("/projects/:projectId/files/:kind", handleMigrationUpload)
		m.POST("/projects/:projectId/validate", handleMigrationValidate)
		m.POST("/projects/:projectId/dry-run", handleMigrationDryRun)
		m.POST("/projects/:projectId/signoff", handleMigrationSignoff)
		m.POST("/projects/:projectId/execute", handleMigrationExecute)
		m.GET("/projects/:projectId/errors", handleMigrationErrors)
		m.POST("/projects/:projectId/rollback", handleMigrationRollback)

		// MIG-007 subscription takeover: review + explicit, audited owner
		// decisions. Import never touches Stripe; these do.
		m.GET("/projects/:projectId/subscriptions", handleMigrationSubscriptionList)
		m.POST("/subscriptions/:subId/activate", handleMigrationSubscriptionActivate)
		m.POST("/subscriptions/:subId/decline", handleMigrationSubscriptionDecline)
	}
}

const migrationExecuteJobType = "migration.execute"

// RegisterMigrationJobs registers the durable execute job — imports run on
// the jobs kernel (retryable, resumable via the SourceObjectMap, survives
// deploys mid-import).
func RegisterMigrationJobs() {
	jobs.Register(migrationExecuteJobType, func(ctx context.Context, job *jobs.Job) error {
		tenantHex, _ := job.Payload["tenant_id"].(string)
		projectPub, _ := job.Payload["project_public_id"].(string)
		if !bson.IsObjectIdHex(tenantHex) || projectPub == "" {
			return nil // malformed payload: nothing to retry
		}
		p, err := migration.LoadProject(bson.ObjectIdHex(tenantHex), projectPub)
		if err != nil {
			return err
		}
		_, err = migration.ExecuteWithJob(p, job.Id, job.LeaseOwner)
		return err
	})
}

var migrationFileKinds = map[string]bool{
	"contacts": true, "products": true, "offers": true,
	"transactions": true, "grants": true, "courses": true, "assets": true,
	"subscriptions": true, "forms": true, "pages": true, "automations": true,
}

func migrationProject(c *gin.Context) (*pkgmodels.MigrationProject, bool) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return nil, false
	}
	p, err := migration.LoadProject(tenantID, c.Param("projectId"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return nil, false
	}
	return p, true
}

func handleMigrationCreate(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	p := pkgmodels.NewMigrationProject(tenantID, migration.SourceKajabi)
	if err := db.GetCollection(pkgmodels.MigrationProjectCollection).Insert(p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create project"})
		return
	}
	c.JSON(http.StatusCreated, p)
}

func handleMigrationList(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	var projects []pkgmodels.MigrationProject
	_ = db.GetCollection(pkgmodels.MigrationProjectCollection).
		Find(bson.M{"tenant_id": tenantID}).Sort("-created_at").All(&projects)
	if projects == nil {
		projects = []pkgmodels.MigrationProject{}
	}
	c.JSON(http.StatusOK, gin.H{"projects": projects})
}

func handleMigrationGet(c *gin.Context) {
	p, ok := migrationProject(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, p)
}

// handleMigrationUpload accepts one export file as the raw request body:
// POST /migration/projects/:id/files/contacts  (body = CSV bytes).
func handleMigrationUpload(c *gin.Context) {
	p, ok := migrationProject(c)
	if !ok {
		return
	}
	kind := c.Param("kind")
	if !migrationFileKinds[kind] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown file kind"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, migration.MaxFileBytes+1))
	if err != nil || len(body) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty upload"})
		return
	}
	if err := migration.StoreFile(p, kind, c.Query("name"), body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stored": kind, "size_bytes": len(body)})
}

func handleMigrationValidate(c *gin.Context) {
	p, ok := migrationProject(c)
	if !ok {
		return
	}
	report, err := migration.Validate(p)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": p.Status, "report": report})
}

func handleMigrationDryRun(c *gin.Context) {
	p, ok := migrationProject(c)
	if !ok {
		return
	}
	report, err := migration.DryRun(p)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": p.Status, "report": report})
}

// handleMigrationSignoff records the audited MIG-011 cutover approval. Only
// a reviewed dry-run may be signed off; execute requires it.
func handleMigrationSignoff(c *gin.Context) {
	p, ok := migrationProject(c)
	if !ok {
		return
	}
	var req struct {
		Confirm bool `json:"confirm"`
	}
	_ = c.ShouldBindJSON(&req)
	if !req.Confirm {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sign-off requires {\"confirm\": true} after reviewing the dry-run preview"})
		return
	}
	if p.Status != pkgmodels.MigrationStatusDryRun {
		c.JSON(http.StatusConflict, gin.H{"error": "run a dry-run first — sign-off approves its preview"})
		return
	}
	now := time.Now()
	actor := auth.GetAccountUserID(c)
	if err := db.GetCollection(pkgmodels.MigrationProjectCollection).UpdateId(p.Id, bson.M{"$set": bson.M{
		"status": pkgmodels.MigrationStatusSignedOff, "signed_off_by": actor,
		"signed_off_at": now, "updated_at": now,
	}}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record sign-off"})
		return
	}
	e := audit.FromContext(c)
	e.Action, e.Outcome = "migration.signoff", "success"
	e.TargetType, e.TargetID = "migration_project", p.PublicId
	audit.Record(e)
	c.JSON(http.StatusOK, gin.H{"status": pkgmodels.MigrationStatusSignedOff})
}

func handleMigrationExecute(c *gin.Context) {
	p, ok := migrationProject(c)
	if !ok {
		return
	}
	if p.Status == pkgmodels.MigrationStatusImporting {
		c.JSON(http.StatusConflict, gin.H{"error": "import already running"})
		return
	}
	// MIG-011: first import of a project requires the sign-off gate. Reruns
	// of an already-completed project (delta imports) keep their sign-off.
	if p.Status != pkgmodels.MigrationStatusSignedOff && p.Status != pkgmodels.MigrationStatusCompleted && p.Status != pkgmodels.MigrationStatusFailed {
		c.JSON(http.StatusConflict, gin.H{"error": "sign-off required — review the dry-run preview and POST /signoff first"})
		return
	}
	job := jobs.NewJob(migrationExecuteJobType,
		"migration:"+p.PublicId+":"+time.Now().Format("20060102T150405"),
		jobs.Envelope{Actor: auth.GetAccountUserID(c), Version: 1},
		bson.M{"tenant_id": p.TenantID.Hex(), "project_public_id": p.PublicId})
	if err := jobs.Enqueue(job); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to enqueue import"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "importing", "job": job.PublicId})
}

func handleMigrationErrors(c *gin.Context) {
	p, ok := migrationProject(c)
	if !ok {
		return
	}
	var errs []pkgmodels.MigrationError
	_ = db.GetCollection(pkgmodels.MigrationErrorCollection).
		Find(bson.M{"project_id": p.Id}).Sort("source_type", "row").Limit(1000).All(&errs)
	if errs == nil {
		errs = []pkgmodels.MigrationError{}
	}
	c.JSON(http.StatusOK, gin.H{"errors": errs})
}

func handleMigrationRollback(c *gin.Context) {
	p, ok := migrationProject(c)
	if !ok {
		return
	}
	if p.Status == pkgmodels.MigrationStatusImporting {
		c.JSON(http.StatusConflict, gin.H{"error": "import running — wait for it to finish"})
		return
	}
	report, err := migration.Rollback(p)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": p.Status, "report": report})
}
