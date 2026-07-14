package routes

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/entitlements"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"

	"github.com/josephalai/sentanyl/pkg/utils"
)

// EnsureEcommerceIndexes creates indexes needed by the ecommerce collections.
// Currently enforces a unique index on (tenant_id, code) for coupons so duplicate
// redeem codes per tenant are rejected at the DB layer in addition to the
// application-level pre-check.
// EnsureServiceFulfillmentIndexes enforces the FUL-001/002 invariants: one
// service enrollment per purchased line item, and one instance slot per
// (enrollment, template) so provisioning upserts can never duplicate slots.
func EnsureServiceFulfillmentIndexes() {
	if err := db.GetCollection(pkgmodels.ServiceEnrollmentCollection).EnsureIndex(mgo.Index{
		Key: []string{"purchase_item_id"}, Unique: true, Sparse: true, Background: true,
	}); err != nil {
		log.Printf("fulfillment: service enrollment purchase-item index: %v", err)
	}
	if err := db.GetCollection(pkgmodels.ServiceInstanceCollection).EnsureIndex(mgo.Index{
		Key: []string{"enrollment_id", "instance_template_id"}, Unique: true, Background: true,
	}); err != nil {
		log.Printf("fulfillment: service instance slot index: %v", err)
	}
}

func EnsureEcommerceIndexes() {
	col := db.GetCollection(pkgmodels.CouponCollection)
	idx := mgo.Index{
		Key:        []string{"tenant_id", "code"},
		Unique:     true,
		Background: true,
	}
	if err := col.EnsureIndex(idx); err != nil {
		log.Printf("ecommerce: failed to ensure coupon unique index: %v", err)
	}

	// Commerce ledger invariants (COM-CC-005/006): one Purchase per Stripe
	// session per tenant, and one PurchaseItem per (purchase, product) — the
	// idempotency keys that keep webhook retries from duplicating commercial
	// records or provisioning.
	ledgerIndexes := map[string]mgo.Index{
		pkgmodels.PurchaseCollection: {
			Key:        []string{"tenant_id", "stripe_session_id"},
			Unique:     true,
			Background: true,
			Sparse:     true,
		},
		pkgmodels.PurchaseItemCollection: {
			Key:        []string{"purchase_id", "product_id"},
			Unique:     true,
			Background: true,
		},
	}
	for coll, index := range ledgerIndexes {
		if err := db.GetCollection(coll).EnsureIndex(index); err != nil {
			log.Printf("ecommerce: failed to ensure %s index %v: %v", coll, index.Key, err)
		}
	}
	// Access-grant lookup index (non-unique) for library authorization (W2-C).
	if err := db.GetCollection(pkgmodels.AccessGrantCollection).EnsureIndex(mgo.Index{
		Key:        []string{"tenant_id", "contact_id", "product_id", "status"},
		Background: true,
	}); err != nil {
		log.Printf("ecommerce: failed to ensure access_grant index: %v", err)
	}
}

// RegisterEcommerceRoutes registers all ecommerce-related endpoints.
func RegisterEcommerceRoutes(rg *gin.RouterGroup) {
	// Tenant-scoped product CRUD
	rg.POST("/products", handleTenantCreateProduct)
	rg.GET("/products", handleTenantListProducts)
	rg.PUT("/products/:id", handleTenantUpdateProduct)
	rg.DELETE("/products/:id", handleTenantDeleteProduct)

	// Digital Download asset management — file uploads, attach/detach, settings.
	RegisterDigitalDownloadTenantRoutes(rg)

	// Service Product fulfillment — clients, instances, notes, resources.
	RegisterServiceTenantRoutes(rg)

	// Offer CRUD
	rg.POST("/offers", handleCreateOffer)
	rg.GET("/offers", handleListOffers)
	rg.PUT("/offers/:id", handleUpdateOffer)
	rg.DELETE("/offers/:id", handleDeleteOffer)

	// Coupon CRUD
	rg.POST("/coupons", handleCreateCoupon)
	rg.GET("/coupons", handleListCoupons)
	rg.PUT("/coupons/:id", handleUpdateCoupon)
	rg.DELETE("/coupons/:id", handleDeleteCoupon)

	// Contacts/CRM
	rg.GET("/contacts", handleListContacts)
	rg.GET("/contacts/:id", handleGetContact)
}

// RegisterCustomerLibraryRoutes registers customer-facing routes.
func RegisterCustomerLibraryRoutes(rg *gin.RouterGroup) {
	rg.GET("/library/products", handleGetLibraryProducts)
	RegisterDigitalDownloadCustomerRoutes(rg)
	RegisterServiceCustomerRoutes(rg)
	RegisterCustomerLibrarySummaryRoutes(rg)
}

// --- Product CRUD (tenant-scoped) ---

func handleTenantCreateProduct(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		Name         string           `json:"name" binding:"required"`
		Description  string           `json:"description"`
		ProductType  string           `json:"product_type"`
		ThumbnailURL string           `json:"thumbnail_url"`
		Status       string           `json:"status"`
		Modules      []*pkgmodels.Module `json:"modules"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	status := "draft"
	if req.Status != "" {
		status = req.Status
	}

	product := &pkgmodels.Product{
		Id:           bson.NewObjectId(),
		PublicId:     utils.GeneratePublicId(),
		TenantID:     tenantID,
		Name:         req.Name,
		Description:  req.Description,
		ProductType:  req.ProductType,
		ThumbnailURL: req.ThumbnailURL,
		Status:       status,
		Modules:      req.Modules,
	}

	if err := db.GetCollection(pkgmodels.ProductCollection).Insert(product); err != nil {
		log.Println("Error creating product:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create product"})
		return
	}

	c.JSON(http.StatusCreated, product)
}

func handleTenantListProducts(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var products []pkgmodels.Product
	err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).All(&products)
	if err != nil {
		log.Println("Error listing products:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list products"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"products": products})
}

func handleTenantUpdateProduct(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	productID := c.Param("id")
	if !bson.IsObjectIdHex(productID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
		return
	}

	var req struct {
		Name         string                          `json:"name"`
		Description  string                          `json:"description"`
		ProductType  string                          `json:"product_type"`
		ThumbnailURL string                          `json:"thumbnail_url"`
		Status       string                          `json:"status"`
		Modules      []*pkgmodels.Module             `json:"modules"`
		Service      *pkgmodels.ServiceConfig        `json:"service"`
		Downloads    *pkgmodels.DigitalDownloadConfig `json:"downloads"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	update := bson.M{}
	if req.Name != "" {
		update["name"] = req.Name
	}
	if req.Description != "" {
		update["description"] = req.Description
	}
	if req.ProductType != "" {
		update["product_type"] = req.ProductType
	}
	if req.ThumbnailURL != "" {
		update["thumbnail_url"] = req.ThumbnailURL
	}
	if req.Status != "" {
		update["status"] = req.Status
	}
	if req.Modules != nil {
		update["modules"] = req.Modules
	}
	if req.Service != nil {
		// Auto-assign ObjectIds to instance templates that arrive without one.
		// Tenant UI sends bare {order,title,description,...} for new rows; the
		// bson driver rejects zero-valued ObjectIds, so we mint them here. The
		// id is what the LMS enroll provisioner stores on each ServiceInstance,
		// so a stable id matters even before publish.
		for _, t := range req.Service.InstanceTemplates {
			if !t.Id.Valid() {
				t.Id = bson.NewObjectId()
			}
		}
		update["service"] = req.Service
	}
	if req.Downloads != nil {
		update["downloads"] = req.Downloads
	}

	if len(update) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}

	err := db.GetCollection(pkgmodels.ProductCollection).Update(
		bson.M{"_id": bson.ObjectIdHex(productID), "tenant_id": tenantID},
		bson.M{"$set": update},
	)
	if err != nil {
		log.Println("Error updating product:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update product"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "product updated"})
}

func handleTenantDeleteProduct(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	productID := c.Param("id")
	if !bson.IsObjectIdHex(productID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
		return
	}

	err := db.GetCollection(pkgmodels.ProductCollection).Update(
		bson.M{"_id": bson.ObjectIdHex(productID), "tenant_id": tenantID},
		bson.M{"$currentDate": bson.M{"timestamps.deleted_at": true}},
	)
	if err != nil {
		log.Println("Error deleting product:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete product"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "product deleted"})
}

// --- Offer CRUD ---

// activeGrantProductIDs returns the product ids a contact holds an active
// Access Grant for (COM-CC-001). This is the authoritative entitlement source.
func activeGrantProductIDs(tenantID, contactID bson.ObjectId) []bson.ObjectId {
	var grants []pkgmodels.AccessGrant
	db.GetCollection(pkgmodels.AccessGrantCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"contact_id": contactID,
		"status":     pkgmodels.GrantStatusActive,
	}).All(&grants)
	out := make([]bson.ObjectId, 0, len(grants))
	for _, g := range grants {
		out = append(out, g.ProductID)
	}
	return out
}

// resolveTenantProducts resolves each supplied identifier (hex ObjectId or
// public_id) to a Product owned by tenantID. It returns the resolved ids and,
// separately, any identifiers that did not resolve to a live product in this
// tenant. A hex id is NOT trusted on its face — it must match a tenant-owned
// product, closing the cross-tenant attach hole (COM-CC-002/003).
func resolveTenantProducts(tenantID bson.ObjectId, ids []string) ([]bson.ObjectId, []string) {
	resolved := make([]bson.ObjectId, 0, len(ids))
	var unresolved []string
	for _, pid := range ids {
		q := bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}
		if bson.IsObjectIdHex(pid) {
			q["_id"] = bson.ObjectIdHex(pid)
		} else {
			q["public_id"] = pid
		}
		var found pkgmodels.Product
		if err := db.GetCollection(pkgmodels.ProductCollection).Find(q).One(&found); err != nil {
			unresolved = append(unresolved, pid)
			continue
		}
		resolved = append(resolved, found.Id)
	}
	return resolved, unresolved
}

func handleCreateOffer(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		Title            string   `json:"title" binding:"required"`
		PricingModel     string   `json:"pricing_model" binding:"required"`
		Amount           int64    `json:"amount"`
		Currency         string   `json:"currency"`
		GrantedBadges    []string                  `json:"granted_badges"`
		IncludedProducts []string                  `json:"included_products"`
		Coaching         *pkgmodels.OfferCoaching  `json:"coaching"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title and pricing_model are required"})
		return
	}

	offer := pkgmodels.NewOffer(req.Title, tenantID)
	offer.PricingModel = req.PricingModel
	offer.Amount = req.Amount
	if req.Currency != "" {
		offer.Currency = req.Currency
	}
	offer.GrantedBadges = req.GrantedBadges
	if req.Coaching != nil && req.Coaching.SessionCount > 0 {
		offer.Coaching = req.Coaching
	}

	// Resolve every included product through a tenant-scoped lookup — a hex id
	// is not trusted unless it belongs to this tenant. Invalid identifiers are
	// rejected with an itemized error rather than silently dropped, and an
	// offer must include at least one product (COM-CC-002/003).
	resolvedProducts, unresolved := resolveTenantProducts(tenantID, req.IncludedProducts)
	if len(unresolved) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":              "one or more included products are not valid for this tenant",
			"unresolved_products": unresolved,
		})
		return
	}
	if len(resolvedProducts) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "an offer must include at least one product"})
		return
	}
	offer.IncludedProducts = resolvedProducts

	if err := db.GetCollection(pkgmodels.OfferCollection).Insert(offer); err != nil {
		log.Println("Error creating offer:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create offer"})
		return
	}

	c.JSON(http.StatusCreated, offer)
}

func handleListOffers(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var offers []pkgmodels.Offer
	err := db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).All(&offers)
	if err != nil {
		log.Println("Error listing offers:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list offers"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"offers": offers})
}

func handleUpdateOffer(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	offerID := c.Param("id")
	if !bson.IsObjectIdHex(offerID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid offer id"})
		return
	}

	var req struct {
		Title            string   `json:"title"`
		PricingModel     string   `json:"pricing_model"`
		Amount           *int64   `json:"amount"`
		Currency         string   `json:"currency"`
		GrantedBadges    []string                 `json:"granted_badges"`
		IncludedProducts []string                 `json:"included_products"`
		Coaching         *pkgmodels.OfferCoaching `json:"coaching"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	update := bson.M{}
	if req.Title != "" {
		update["title"] = req.Title
	}
	if req.PricingModel != "" {
		update["pricing_model"] = req.PricingModel
	}
	if req.Amount != nil {
		update["amount"] = *req.Amount
	}
	if req.Currency != "" {
		update["currency"] = req.Currency
	}
	if req.GrantedBadges != nil {
		update["granted_badges"] = req.GrantedBadges
	}
	if req.Coaching != nil {
		if req.Coaching.SessionCount > 0 {
			update["coaching"] = req.Coaching
		} else {
			update["coaching"] = nil
		}
	}
	if req.IncludedProducts != nil {
		// Tenant-scoped resolution with itemized rejection (COM-CC-002/003).
		included, unresolved := resolveTenantProducts(tenantID, req.IncludedProducts)
		if len(unresolved) > 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":              "one or more included products are not valid for this tenant",
				"unresolved_products": unresolved,
			})
			return
		}
		if len(included) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "an offer must include at least one product"})
			return
		}
		update["included_products"] = included
	}

	if len(update) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}

	err := db.GetCollection(pkgmodels.OfferCollection).Update(
		bson.M{"_id": bson.ObjectIdHex(offerID), "tenant_id": tenantID},
		bson.M{"$set": update},
	)
	if err != nil {
		log.Println("Error updating offer:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update offer"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "offer updated"})
}

func handleDeleteOffer(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	offerID := c.Param("id")
	if !bson.IsObjectIdHex(offerID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid offer id"})
		return
	}

	// COM-CC-004: an offer referenced by commercial history must not be
	// physically destroyed. Block deletion when a subscription/purchase points
	// at it, and otherwise archive (soft-delete) so historical purchase and
	// access snapshots keep a resolvable definition.
	oid := bson.ObjectIdHex(offerID)
	refs, _ := db.GetCollection(pkgmodels.SubscriptionCollection).Find(bson.M{
		"tenant_id": tenantID, "offer_id": oid,
	}).Count()
	if refs > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "offer is referenced by purchases and cannot be deleted; archive it instead"})
		return
	}

	now := time.Now()
	err := db.GetCollection(pkgmodels.OfferCollection).Update(
		bson.M{"_id": oid, "tenant_id": tenantID},
		bson.M{"$set": bson.M{"timestamps.deleted_at": now}},
	)
	if err != nil {
		log.Println("Error deleting offer:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete offer"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "offer archived"})
}

// --- Coupon CRUD ---

func handleCreateCoupon(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		Code           string `json:"code" binding:"required"`
		DiscountType   string `json:"discount_type" binding:"required"`
		Value          int64  `json:"value" binding:"required"`
		Duration       string `json:"duration"`
		MaxRedemptions int    `json:"max_redemptions"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code, discount_type, and value are required"})
		return
	}

	existing, err := db.GetCollection(pkgmodels.CouponCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"code":                  req.Code,
		"timestamps.deleted_at": nil,
	}).Count()
	if err != nil {
		log.Println("Error checking coupon uniqueness:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create coupon"})
		return
	}
	if existing > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "coupon code already exists"})
		return
	}

	coupon := pkgmodels.NewCoupon(req.Code, tenantID)
	coupon.DiscountType = req.DiscountType
	coupon.Value = req.Value
	coupon.Duration = req.Duration
	coupon.MaxRedemptions = req.MaxRedemptions

	if err := db.GetCollection(pkgmodels.CouponCollection).Insert(coupon); err != nil {
		if mgo.IsDup(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "coupon code already exists"})
			return
		}
		log.Println("Error creating coupon:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create coupon"})
		return
	}

	c.JSON(http.StatusCreated, coupon)
}

func handleUpdateCoupon(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	couponID := c.Param("id")
	if !bson.IsObjectIdHex(couponID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid coupon id"})
		return
	}

	var req struct {
		Code           *string `json:"code"`
		DiscountType   *string `json:"discount_type"`
		Value          *int64  `json:"value"`
		Duration       *string `json:"duration"`
		MaxRedemptions *int    `json:"max_redemptions"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	update := bson.M{}
	if req.Code != nil && *req.Code != "" {
		update["code"] = *req.Code
	}
	if req.DiscountType != nil && *req.DiscountType != "" {
		update["discount_type"] = *req.DiscountType
	}
	if req.Value != nil {
		update["value"] = *req.Value
	}
	if req.Duration != nil {
		update["duration"] = *req.Duration
	}
	if req.MaxRedemptions != nil {
		update["max_redemptions"] = *req.MaxRedemptions
	}
	if len(update) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}

	err := db.GetCollection(pkgmodels.CouponCollection).Update(
		bson.M{"_id": bson.ObjectIdHex(couponID), "tenant_id": tenantID},
		bson.M{"$set": update},
	)
	if err != nil {
		if mgo.IsDup(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "coupon code already exists"})
			return
		}
		log.Println("Error updating coupon:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update coupon"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "coupon updated"})
}

func handleDeleteCoupon(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	couponID := c.Param("id")
	if !bson.IsObjectIdHex(couponID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid coupon id"})
		return
	}

	err := db.GetCollection(pkgmodels.CouponCollection).Update(
		bson.M{"_id": bson.ObjectIdHex(couponID), "tenant_id": tenantID},
		bson.M{"$currentDate": bson.M{"timestamps.deleted_at": true}},
	)
	if err != nil {
		log.Println("Error deleting coupon:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete coupon"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "coupon deleted"})
}

func handleListCoupons(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var coupons []pkgmodels.Coupon
	err := db.GetCollection(pkgmodels.CouponCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).All(&coupons)
	if err != nil {
		log.Println("Error listing coupons:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list coupons"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"coupons": coupons})
}

// --- Contacts/CRM ---

func handleListContacts(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var contacts []pkgmodels.User
	err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).All(&contacts)
	if err != nil {
		log.Println("Error listing contacts:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list contacts"})
		return
	}

	var result []gin.H
	for _, contact := range contacts {
		result = append(result, gin.H{
			"id":            contact.Id.Hex(),
			"email":         string(contact.Email),
			"name":          contact.Name,
			"subscribed":    contact.Subscribed,
			"badges":        contact.Badges,
			"custom_fields": contact.CustomFields,
			"phone":         contact.Phone,
		})
	}

	c.JSON(http.StatusOK, gin.H{"contacts": result})
}

func handleGetContact(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	contactID := c.Param("id")
	if !bson.IsObjectIdHex(contactID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid contact id"})
		return
	}

	var contact pkgmodels.User
	err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
		"_id":       bson.ObjectIdHex(contactID),
		"tenant_id": tenantID,
	}).One(&contact)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "contact not found"})
		return
	}

	var purchases []pkgmodels.PurchaseLog
	db.GetCollection(pkgmodels.PurchaseLogCollection).Find(bson.M{
		"user_id":   contact.Id,
		"tenant_id": tenantID,
	}).All(&purchases)

	c.JSON(http.StatusOK, gin.H{
		"contact": gin.H{
			"id":            contact.Id.Hex(),
			"email":         string(contact.Email),
			"name":          contact.Name,
			"subscribed":    contact.Subscribed,
			"badges":        contact.Badges,
			"custom_fields": contact.CustomFields,
			"phone":         contact.Phone,
		},
		"purchases": purchases,
	})
}

// --- Customer Library ---

func handleGetLibraryProducts(c *gin.Context) {
	contactIDStr := auth.GetContactID(c)
	tenantIDStr := auth.GetTenantID(c)
	if contactIDStr == "" || tenantIDStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	if !bson.IsObjectIdHex(contactIDStr) || !bson.IsObjectIdHex(tenantIDStr) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid token data"})
		return
	}

	contactID := bson.ObjectIdHex(contactIDStr)
	tenantID := bson.ObjectIdHex(tenantIDStr)

	var contact pkgmodels.User
	err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
		"_id":       contactID,
		"tenant_id": tenantID,
	}).One(&contact)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "contact not found"})
		return
	}

	// DEL-001: one shared authority answers every delivery surface — active
	// Access Grants (COM-CC-001) unioned with the transitional badge path
	// until ACCESS_GRANTS_ONLY=1 flips grants to the sole authority.
	productIDs := entitlements.EntitledProductIDs(tenantID, contactID)

	if len(productIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"products": []interface{}{}})
		return
	}

	var products []pkgmodels.Product
	db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"_id":                   bson.M{"$in": productIDs},
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).All(&products)

	locale := resolveRequestLocale(c, tenantID, contactID)
	for i := range products {
		title, description := applyProductTranslation(&products[i], locale)
		products[i].Name = title
		products[i].Description = description
		for j := range products[i].CourseModules {
			products[i].CourseModules[j].Title = applyModuleTranslation(products[i].CourseModules[j], locale)
		}
	}

	c.JSON(http.StatusOK, gin.H{"products": products})
}
