package routes

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	
	"github.com/josephalai/sentanyl/pkg/utils"
)

// RegisterEcommerceRoutes registers all ecommerce-related endpoints.
func RegisterEcommerceRoutes(rg *gin.RouterGroup) {
	// Tenant-scoped product CRUD
	rg.POST("/products", handleTenantCreateProduct)
	rg.GET("/products", handleTenantListProducts)
	rg.PUT("/products/:id", handleTenantUpdateProduct)
	rg.DELETE("/products/:id", handleTenantDeleteProduct)

	// Offer CRUD
	rg.POST("/offers", handleCreateOffer)
	rg.GET("/offers", handleListOffers)
	rg.PUT("/offers/:id", handleUpdateOffer)
	rg.DELETE("/offers/:id", handleDeleteOffer)

	// Coupon CRUD
	rg.POST("/coupons", handleCreateCoupon)
	rg.GET("/coupons", handleListCoupons)

	// Contacts/CRM
	rg.GET("/contacts", handleListContacts)
	rg.GET("/contacts/:id", handleGetContact)
}

// RegisterCustomerLibraryRoutes registers customer-facing routes.
func RegisterCustomerLibraryRoutes(rg *gin.RouterGroup) {
	rg.GET("/library/products", handleGetLibraryProducts)
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
		Name         string           `json:"name"`
		Description  string           `json:"description"`
		ProductType  string           `json:"product_type"`
		ThumbnailURL string           `json:"thumbnail_url"`
		Status       string           `json:"status"`
		Modules      []*pkgmodels.Module `json:"modules"`
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
		GrantedBadges    []string `json:"granted_badges"`
		IncludedProducts []string `json:"included_products"`
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

	for _, pid := range req.IncludedProducts {
		if bson.IsObjectIdHex(pid) {
			offer.IncludedProducts = append(offer.IncludedProducts, bson.ObjectIdHex(pid))
		}
	}

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
		Title         string   `json:"title"`
		PricingModel  string   `json:"pricing_model"`
		Amount        *int64   `json:"amount"`
		Currency      string   `json:"currency"`
		GrantedBadges []string `json:"granted_badges"`
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

	err := db.GetCollection(pkgmodels.OfferCollection).Remove(
		bson.M{"_id": bson.ObjectIdHex(offerID), "tenant_id": tenantID},
	)
	if err != nil {
		log.Println("Error deleting offer:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete offer"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "offer deleted"})
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

	coupon := pkgmodels.NewCoupon(req.Code, tenantID)
	coupon.DiscountType = req.DiscountType
	coupon.Value = req.Value
	coupon.Duration = req.Duration
	coupon.MaxRedemptions = req.MaxRedemptions

	if err := db.GetCollection(pkgmodels.CouponCollection).Insert(coupon); err != nil {
		log.Println("Error creating coupon:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create coupon"})
		return
	}

	c.JSON(http.StatusCreated, coupon)
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

	var badgeNames []string
	for _, badgeID := range contact.Badges {
		var badge pkgmodels.Badge
		err := db.GetCollection(pkgmodels.BadgeCollection).FindId(badgeID).One(&badge)
		if err == nil {
			badgeNames = append(badgeNames, badge.Name)
		}
	}

	if len(badgeNames) == 0 {
		c.JSON(http.StatusOK, gin.H{"products": []interface{}{}})
		return
	}

	var offers []pkgmodels.Offer
	db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"granted_badges":        bson.M{"$in": badgeNames},
		"timestamps.deleted_at": nil,
	}).All(&offers)

	productIDSet := make(map[bson.ObjectId]bool)
	for _, offer := range offers {
		for _, pid := range offer.IncludedProducts {
			productIDSet[pid] = true
		}
	}

	var productIDs []bson.ObjectId
	for pid := range productIDSet {
		productIDs = append(productIDs, pid)
	}

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

	c.JSON(http.StatusOK, gin.H{"products": products})
}
