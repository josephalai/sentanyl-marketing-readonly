package routes

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// RegisterServiceTenantRoutes registers tenant-side routes for the Service
// Product type (non-video, async-fulfilled package of N instances). Mounted
// alongside the existing product/offer/coupon CRUD; caller has already
// applied RequireTenantAuth on the group.
func RegisterServiceTenantRoutes(rg *gin.RouterGroup) {
	rg.GET("/services/programs/:productId/clients", handleListServiceClients)
	rg.GET("/services/enrollments/:enrollmentId", handleGetServiceEnrollment)
	rg.GET("/services/instances/:instanceId", handleGetServiceInstance)
	rg.POST("/services/instances/:instanceId/notes", handleCreateServiceNote)
	rg.PUT("/services/instances/:instanceId/notes/:noteId", handleUpdateServiceNote)
	rg.DELETE("/services/instances/:instanceId/notes/:noteId", handleDeleteServiceNote)
	rg.POST("/services/instances/:instanceId/resources/upload-url", handleServiceResourceUploadURL)
	rg.POST("/services/instances/:instanceId/resources/attach", handleServiceResourceAttach)
	rg.DELETE("/services/instances/:instanceId/resources/:resourceId", handleServiceResourceDelete)
	rg.POST("/services/instances/:instanceId/start", handleStartServiceInstance)
	rg.POST("/services/instances/:instanceId/complete", handleCompleteServiceInstance)
}

// RegisterServiceCustomerRoutes registers customer-facing routes that drive
// the per-instance dashboard. Caller has already applied RequireCustomerAuth.
func RegisterServiceCustomerRoutes(rg *gin.RouterGroup) {
	rg.GET("/services/:productId", handleCustomerServiceDashboard)
	rg.GET("/services/instances/:instanceId", handleCustomerGetServiceInstance)
	rg.POST("/services/instances/:instanceId/notes", handleCustomerCreateServiceNote)
	rg.GET("/services/instances/:instanceId/resources/:resourceId/url", handleCustomerServiceResourceURL)
}

// --- Tenant: program clients ---

func handleListServiceClients(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	productID := c.Param("productId")
	if !bson.IsObjectIdHex(productID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
		return
	}
	pid := bson.ObjectIdHex(productID)

	var enrollments []pkgmodels.ServiceEnrollment
	if err := db.GetCollection(pkgmodels.ServiceEnrollmentCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"product_id":            pid,
		"timestamps.deleted_at": nil,
	}).All(&enrollments); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load enrollments"})
		return
	}

	contactIDs := make([]bson.ObjectId, 0, len(enrollments))
	for _, e := range enrollments {
		contactIDs = append(contactIDs, e.ContactID)
	}
	contacts := loadContactsByIDs(tenantID, contactIDs)

	type item struct {
		EnrollmentID    string  `json:"enrollment_id"`
		PublicID        string  `json:"public_id"`
		ContactID       string  `json:"contact_id"`
		ContactEmail    string  `json:"contact_email,omitempty"`
		ContactName     string  `json:"contact_name,omitempty"`
		Status          string  `json:"status"`
		InstancesTotal  int     `json:"instances_total"`
		InstancesDone   int     `json:"instances_done"`
		EnrolledAt      string  `json:"enrolled_at,omitempty"`
		CompletedAt     *string `json:"completed_at,omitempty"`
	}
	out := make([]item, 0, len(enrollments))
	for _, e := range enrollments {
		row := item{
			EnrollmentID:   e.Id.Hex(),
			PublicID:       e.PublicId,
			ContactID:      e.ContactID.Hex(),
			Status:         e.Status,
			InstancesTotal: e.InstancesTotal,
			InstancesDone:  e.InstancesDone,
			EnrolledAt:     e.EnrolledAt.Format(time.RFC3339),
		}
		if e.CompletedAt != nil {
			ts := e.CompletedAt.Format(time.RFC3339)
			row.CompletedAt = &ts
		}
		if u, ok := contacts[e.ContactID]; ok {
			row.ContactEmail = string(u.Email)
			row.ContactName = strings.TrimSpace(u.Name.First + " " + u.Name.Last)
		}
		out = append(out, row)
	}

	c.JSON(http.StatusOK, gin.H{"clients": out})
}

func loadContactsByIDs(tenantID bson.ObjectId, ids []bson.ObjectId) map[bson.ObjectId]pkgmodels.User {
	out := map[bson.ObjectId]pkgmodels.User{}
	if len(ids) == 0 {
		return out
	}
	var contacts []pkgmodels.User
	_ = db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
		"_id":       bson.M{"$in": ids},
		"tenant_id": tenantID,
	}).All(&contacts)
	for _, u := range contacts {
		out[u.Id] = u
	}
	return out
}

// --- Tenant: enrollment + instance detail ---

func handleGetServiceEnrollment(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	enrollment, status, msg := loadOwnedEnrollment(tenantID, c.Param("enrollmentId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	instances := loadInstancesForEnrollment(tenantID, enrollment.Id)
	contact := loadContactByID(tenantID, enrollment.ContactID)
	c.JSON(http.StatusOK, gin.H{
		"enrollment": enrollment,
		"instances":  instances,
		"contact":    contact,
	})
}

func handleGetServiceInstance(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	instance, status, msg := loadOwnedInstance(tenantID, c.Param("instanceId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	notes := loadNotesForInstance(tenantID, instance.Id, true)
	resources := loadResourcesForInstance(tenantID, instance.Id)
	c.JSON(http.StatusOK, gin.H{
		"instance":  instance,
		"notes":     notes,
		"resources": resources,
	})
}

// --- Tenant: notes ---

func handleCreateServiceNote(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	instance, status, msg := loadOwnedInstance(tenantID, c.Param("instanceId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}

	var req struct {
		Body       string `json:"body" binding:"required"`
		Visibility string `json:"visibility"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body required"})
		return
	}
	visibility := pkgmodels.ServiceNoteVisibilityShared
	if req.Visibility == pkgmodels.ServiceNoteVisibilityPrivate {
		visibility = pkgmodels.ServiceNoteVisibilityPrivate
	}

	note := pkgmodels.ServiceNote{
		Id:         bson.NewObjectId(),
		TenantID:   tenantID,
		InstanceID: instance.Id,
		ContactID:  instance.ContactID,
		AuthoredBy: pkgmodels.ServiceNoteAuthorTenant,
		Visibility: visibility,
		Body:       req.Body,
	}
	if err := db.GetCollection(pkgmodels.ServiceNoteCollection).Insert(&note); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save note"})
		return
	}
	c.JSON(http.StatusCreated, note)
}

func handleUpdateServiceNote(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	noteID := c.Param("noteId")
	if !bson.IsObjectIdHex(noteID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid note id"})
		return
	}
	var req struct {
		Body       *string `json:"body"`
		Visibility *string `json:"visibility"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	update := bson.M{}
	if req.Body != nil {
		update["body"] = *req.Body
	}
	if req.Visibility != nil {
		v := *req.Visibility
		if v == pkgmodels.ServiceNoteVisibilityPrivate || v == pkgmodels.ServiceNoteVisibilityShared {
			update["visibility"] = v
		}
	}
	if len(update) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no updates"})
		return
	}
	if err := db.GetCollection(pkgmodels.ServiceNoteCollection).Update(
		bson.M{"_id": bson.ObjectIdHex(noteID), "tenant_id": tenantID, "authored_by": pkgmodels.ServiceNoteAuthorTenant},
		bson.M{"$set": update},
	); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "note not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "updated"})
}

func handleDeleteServiceNote(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	noteID := c.Param("noteId")
	if !bson.IsObjectIdHex(noteID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid note id"})
		return
	}
	if err := db.GetCollection(pkgmodels.ServiceNoteCollection).Update(
		bson.M{"_id": bson.ObjectIdHex(noteID), "tenant_id": tenantID, "authored_by": pkgmodels.ServiceNoteAuthorTenant},
		bson.M{"$currentDate": bson.M{"timestamps.deleted_at": true}},
	); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "note not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// --- Tenant: resources ---

func handleServiceResourceUploadURL(c *gin.Context) {
	if downloadStorage == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage not configured"})
		return
	}
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	instance, status, msg := loadOwnedInstance(tenantID, c.Param("instanceId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}

	var req struct {
		FileName    string `json:"file_name" binding:"required"`
		ContentType string `json:"content_type" binding:"required"`
		SizeBytes   int64  `json:"size_bytes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file_name and content_type are required"})
		return
	}
	if !allowedDownloadMime[req.ContentType] {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "file type not allowed"})
		return
	}

	objectPath := fmt.Sprintf("%s/services/%s/instances/%s/%s_%s",
		tenantID.Hex(),
		instance.ProductID.Hex(),
		instance.PublicId,
		utils.GeneratePublicId(),
		safeFileName(req.FileName),
	)
	signed, err := downloadStorage.GenerateUploadURL(downloadBucket, objectPath, req.ContentType)
	if err != nil {
		log.Printf("services: signed upload URL failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to issue upload URL"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"upload_url":   signed,
		"object_path":  objectPath,
		"bucket":       downloadBucket,
		"content_type": req.ContentType,
	})
}

func handleServiceResourceAttach(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	instance, status, msg := loadOwnedInstance(tenantID, c.Param("instanceId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}

	var req struct {
		FileName    string `json:"file_name" binding:"required"`
		ObjectPath  string `json:"object_path" binding:"required"`
		ContentType string `json:"content_type"`
		SizeBytes   int64  `json:"size_bytes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file_name and object_path are required"})
		return
	}
	publicURL := fmt.Sprintf("https://storage.googleapis.com/%s/%s", downloadBucket, req.ObjectPath)
	resource := pkgmodels.NewServiceInstanceResource(tenantID, instance.ProductID, instance.Id, req.FileName, publicURL, req.ObjectPath, req.ContentType, req.SizeBytes)
	resource.UploadedBy = "tenant"
	if err := db.GetCollection(pkgmodels.ServiceInstanceResourceCollection).Insert(resource); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save resource"})
		return
	}
	c.JSON(http.StatusCreated, resource)
}

func handleServiceResourceDelete(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	resourceID := c.Param("resourceId")
	if !bson.IsObjectIdHex(resourceID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid resource id"})
		return
	}
	var resource pkgmodels.ServiceInstanceResource
	if err := db.GetCollection(pkgmodels.ServiceInstanceResourceCollection).Find(bson.M{
		"_id":       bson.ObjectIdHex(resourceID),
		"tenant_id": tenantID,
	}).One(&resource); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return
	}
	if err := db.GetCollection(pkgmodels.ServiceInstanceResourceCollection).Update(
		bson.M{"_id": resource.Id},
		bson.M{"$currentDate": bson.M{"timestamps.deleted_at": true}},
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete resource"})
		return
	}
	if downloadStorage != nil && resource.ObjectPath != "" {
		if err := downloadStorage.DeleteObject(downloadBucket, resource.ObjectPath); err != nil {
			log.Printf("services: GCS delete failed: %v", err)
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// --- Tenant: lifecycle ---

func handleStartServiceInstance(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	instance, status, msg := loadOwnedInstance(tenantID, c.Param("instanceId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	now := time.Now()
	if err := db.GetCollection(pkgmodels.ServiceInstanceCollection).Update(
		bson.M{"_id": instance.Id, "tenant_id": tenantID},
		bson.M{"$set": bson.M{
			"status":     pkgmodels.ServiceInstanceStatusInProgress,
			"started_at": now,
		}},
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "in_progress"})
}

func handleCompleteServiceInstance(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	instance, status, msg := loadOwnedInstance(tenantID, c.Param("instanceId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	if instance.Status == pkgmodels.ServiceInstanceStatusCompleted {
		c.JSON(http.StatusOK, gin.H{"status": "already_completed"})
		return
	}
	now := time.Now()
	if err := db.GetCollection(pkgmodels.ServiceInstanceCollection).Update(
		bson.M{"_id": instance.Id, "tenant_id": tenantID},
		bson.M{"$set": bson.M{
			"status":       pkgmodels.ServiceInstanceStatusCompleted,
			"completed_at": now,
		}},
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to complete"})
		return
	}

	// Bump the parent enrollment's instances_done; if we just finished the
	// last instance, stamp the enrollment CompletedAt so the customer
	// dashboard can render the "all done" state and downstream automations
	// can fire on a single signal.
	doneCount, _ := db.GetCollection(pkgmodels.ServiceInstanceCollection).Find(bson.M{
		"enrollment_id": instance.EnrollmentID,
		"tenant_id":     tenantID,
		"status":        pkgmodels.ServiceInstanceStatusCompleted,
	}).Count()

	enrollUpdate := bson.M{"instances_done": doneCount}
	var enroll pkgmodels.ServiceEnrollment
	if err := db.GetCollection(pkgmodels.ServiceEnrollmentCollection).FindId(instance.EnrollmentID).One(&enroll); err == nil {
		if doneCount >= enroll.InstancesTotal && enroll.CompletedAt == nil {
			enrollUpdate["completed_at"] = now
		}
	}
	_ = db.GetCollection(pkgmodels.ServiceEnrollmentCollection).Update(
		bson.M{"_id": instance.EnrollmentID, "tenant_id": tenantID},
		bson.M{"$set": enrollUpdate},
	)

	c.JSON(http.StatusOK, gin.H{"status": "completed", "instances_done": doneCount})
}

// --- Customer: dashboard + instance + notes + resource URL ---

func handleCustomerServiceDashboard(c *gin.Context) {
	tenantID, contactID, ok := requireCustomer(c)
	if !ok {
		return
	}
	productIDParam := c.Param("productId")
	if !bson.IsObjectIdHex(productIDParam) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
		return
	}
	productID := bson.ObjectIdHex(productIDParam)
	if err := assertContactEntitled(tenantID, contactID, productID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	var product pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"_id":                   productID,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&product); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	if product.ProductType != pkgmodels.ProductTypeService {
		c.JSON(http.StatusConflict, gin.H{"error": "product is not a service"})
		return
	}

	var enrollment pkgmodels.ServiceEnrollment
	if err := db.GetCollection(pkgmodels.ServiceEnrollmentCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"contact_id": contactID,
		"product_id": productID,
	}).One(&enrollment); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "enrollment not found"})
		return
	}
	instances := loadInstancesForEnrollment(tenantID, enrollment.Id)

	c.JSON(http.StatusOK, gin.H{
		"product": gin.H{
			"id":            product.Id.Hex(),
			"public_id":     product.PublicId,
			"name":          product.Name,
			"description":   product.Description,
			"thumbnail_url": product.ThumbnailURL,
		},
		"enrollment": enrollment,
		"instances":  instances,
	})
}

func handleCustomerGetServiceInstance(c *gin.Context) {
	tenantID, contactID, ok := requireCustomer(c)
	if !ok {
		return
	}
	instance, status, msg := loadInstanceForCustomer(tenantID, contactID, c.Param("instanceId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	notes := loadNotesForInstance(tenantID, instance.Id, false)
	resources := loadResourcesForInstance(tenantID, instance.Id)
	c.JSON(http.StatusOK, gin.H{
		"instance":  instance,
		"notes":     notes,
		"resources": resources,
	})
}

func handleCustomerCreateServiceNote(c *gin.Context) {
	tenantID, contactID, ok := requireCustomer(c)
	if !ok {
		return
	}
	instance, status, msg := loadInstanceForCustomer(tenantID, contactID, c.Param("instanceId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	var req struct {
		Body string `json:"body" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body required"})
		return
	}
	note := pkgmodels.ServiceNote{
		Id:         bson.NewObjectId(),
		TenantID:   tenantID,
		InstanceID: instance.Id,
		ContactID:  contactID,
		AuthoredBy: pkgmodels.ServiceNoteAuthorClient,
		Visibility: pkgmodels.ServiceNoteVisibilityShared,
		Body:       req.Body,
	}
	if err := db.GetCollection(pkgmodels.ServiceNoteCollection).Insert(&note); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save note"})
		return
	}
	c.JSON(http.StatusCreated, note)
}

func handleCustomerServiceResourceURL(c *gin.Context) {
	if downloadStorage == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage not configured"})
		return
	}
	tenantID, contactID, ok := requireCustomer(c)
	if !ok {
		return
	}
	instance, status, msg := loadInstanceForCustomer(tenantID, contactID, c.Param("instanceId"))
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	resourceID := c.Param("resourceId")
	if !bson.IsObjectIdHex(resourceID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid resource id"})
		return
	}
	var resource pkgmodels.ServiceInstanceResource
	if err := db.GetCollection(pkgmodels.ServiceInstanceResourceCollection).Find(bson.M{
		"_id":                   bson.ObjectIdHex(resourceID),
		"instance_id":           instance.Id,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&resource); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return
	}
	if resource.ObjectPath == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "resource missing object path"})
		return
	}
	signed, err := downloadStorage.GenerateSignedDownloadURL(downloadBucket, resource.ObjectPath, 60*time.Second)
	if err != nil {
		log.Printf("services: sign GET failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to issue download URL"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"url":        signed,
		"file_name":  resource.Name,
		"file_type":  resource.ContentType,
		"file_size":  resource.SizeBytes,
		"expires_in": 60,
	})
}

// --- Helpers ---

func loadOwnedEnrollment(tenantID bson.ObjectId, idParam string) (*pkgmodels.ServiceEnrollment, int, string) {
	q := bson.M{"tenant_id": tenantID}
	if bson.IsObjectIdHex(idParam) {
		q["_id"] = bson.ObjectIdHex(idParam)
	} else {
		q["public_id"] = idParam
	}
	var e pkgmodels.ServiceEnrollment
	if err := db.GetCollection(pkgmodels.ServiceEnrollmentCollection).Find(q).One(&e); err != nil {
		return nil, http.StatusNotFound, "enrollment not found"
	}
	return &e, 0, ""
}

func loadOwnedInstance(tenantID bson.ObjectId, idParam string) (*pkgmodels.ServiceInstance, int, string) {
	q := bson.M{"tenant_id": tenantID}
	if bson.IsObjectIdHex(idParam) {
		q["_id"] = bson.ObjectIdHex(idParam)
	} else {
		q["public_id"] = idParam
	}
	var i pkgmodels.ServiceInstance
	if err := db.GetCollection(pkgmodels.ServiceInstanceCollection).Find(q).One(&i); err != nil {
		return nil, http.StatusNotFound, "instance not found"
	}
	return &i, 0, ""
}

func loadInstanceForCustomer(tenantID, contactID bson.ObjectId, idParam string) (*pkgmodels.ServiceInstance, int, string) {
	q := bson.M{"tenant_id": tenantID, "contact_id": contactID}
	if bson.IsObjectIdHex(idParam) {
		q["_id"] = bson.ObjectIdHex(idParam)
	} else {
		q["public_id"] = idParam
	}
	var i pkgmodels.ServiceInstance
	if err := db.GetCollection(pkgmodels.ServiceInstanceCollection).Find(q).One(&i); err != nil {
		return nil, http.StatusNotFound, "instance not found"
	}
	return &i, 0, ""
}

func loadInstancesForEnrollment(tenantID, enrollmentID bson.ObjectId) []pkgmodels.ServiceInstance {
	var instances []pkgmodels.ServiceInstance
	_ = db.GetCollection(pkgmodels.ServiceInstanceCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"enrollment_id":         enrollmentID,
		"timestamps.deleted_at": nil,
	}).Sort("order", "_id").All(&instances)
	return instances
}

// loadNotesForInstance returns the notes attached to one instance. tenantView
// is true for the admin dashboard (returns every note regardless of
// visibility) and false for the customer dashboard (filters to shared notes
// only — tenant_private notes never cross the boundary).
func loadNotesForInstance(tenantID, instanceID bson.ObjectId, tenantView bool) []pkgmodels.ServiceNote {
	q := bson.M{
		"tenant_id":             tenantID,
		"instance_id":           instanceID,
		"timestamps.deleted_at": nil,
	}
	if !tenantView {
		q["visibility"] = pkgmodels.ServiceNoteVisibilityShared
	}
	var notes []pkgmodels.ServiceNote
	_ = db.GetCollection(pkgmodels.ServiceNoteCollection).Find(q).All(&notes)
	return notes
}

func loadResourcesForInstance(tenantID, instanceID bson.ObjectId) []pkgmodels.ServiceInstanceResource {
	var resources []pkgmodels.ServiceInstanceResource
	_ = db.GetCollection(pkgmodels.ServiceInstanceResourceCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"instance_id":           instanceID,
		"timestamps.deleted_at": nil,
	}).All(&resources)
	return resources
}

func loadContactByID(tenantID, contactID bson.ObjectId) gin.H {
	var u pkgmodels.User
	if err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
		"_id":       contactID,
		"tenant_id": tenantID,
	}).One(&u); err != nil {
		return gin.H{}
	}
	return gin.H{
		"id":    u.Id.Hex(),
		"email": string(u.Email),
		"name":  strings.TrimSpace(u.Name.First + " " + u.Name.Last),
	}
}

// Avoid unused-import flagging in builds where some helpers above aren't yet
// referenced (kept for symmetry with the coaching skeleton).
var _ = errors.New
