package routes

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/storage"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// downloadStorage is the package-level storage provider used by the downloads
// routes. Set once at startup from main via SetDownloadsStorage. nil means
// uploads + signed URLs return 503 with a clear "GCS not configured" message
// so dev environments without ADC don't silently fall back to disk.
var (
	downloadStorage storage.StorageProvider
	downloadBucket  string
)

// SetDownloadsStorage wires the storage provider used to issue signed upload
// URLs, fetch download URLs, and stream zip bundles. Called from
// marketing-service/cmd/main.go after GCS init.
func SetDownloadsStorage(p storage.StorageProvider, bucket string) {
	downloadStorage = p
	downloadBucket = bucket
}

// RegisterDigitalDownloadTenantRoutes registers the tenant-side routes for
// managing files attached to a digital_download Product. Caller must have
// already applied RequireTenantAuth on the group.
func RegisterDigitalDownloadTenantRoutes(rg *gin.RouterGroup) {
	rg.POST("/products/:id/downloads/upload-url", handleDownloadUploadURL)
	rg.POST("/products/:id/downloads/attach", handleDownloadAttach)
	rg.GET("/products/:id/downloads", handleDownloadList)
	rg.DELETE("/products/:id/downloads/:assetId", handleDownloadDelete)
	rg.PUT("/products/:id/downloads/settings", handleDownloadSettings)
}

// RegisterDigitalDownloadCustomerRoutes registers customer-facing delivery
// routes. Caller must have already applied RequireCustomerAuth.
func RegisterDigitalDownloadCustomerRoutes(rg *gin.RouterGroup) {
	rg.GET("/downloads/:productId", handleCustomerDownloadList)
	rg.GET("/downloads/:productId/assets/:assetId/url", handleCustomerDownloadURL)
	rg.GET("/downloads/:productId/zip", handleCustomerDownloadZip)
}

// allowedDownloadMime is the gateway allow-list. Mirrors the contract in
// docs/features/products/all.md — explicitly blocks SVG, executables, and
// font files that would let malicious tenants smuggle XSS or malware.
var allowedDownloadMime = map[string]bool{
	"application/pdf":                                                          true,
	"application/msword":                                                       true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":  true,
	"application/vnd.ms-excel":                                                 true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":        true,
	"application/vnd.ms-powerpoint":                                            true,
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": true,
	"text/plain":         true,
	"text/csv":           true,
	"application/rtf":    true,
	"application/zip":    true,
	"image/jpeg":         true,
	"image/png":          true,
	"image/gif":          true,
	"image/webp":         true,
	"audio/mpeg":         true,
	"audio/mp3":          true,
	"audio/wav":          true,
	"audio/x-wav":        true,
	"audio/mp4":          true,
	"audio/ogg":          true,
	"audio/flac":         true,
	"video/mp4":          true,
	"video/quicktime":    true,
	"video/webm":         true,
	"video/x-msvideo":    true,
}

// loadOwnedProduct returns the product if it belongs to tenantID and is the
// digital_download type. Returns a typed status code so handlers can keep
// their bodies tight.
func loadOwnedProduct(tenantID bson.ObjectId, productID string, requireType string) (*pkgmodels.Product, int, string) {
	if !bson.IsObjectIdHex(productID) {
		return nil, http.StatusBadRequest, "invalid product id"
	}
	var p pkgmodels.Product
	err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"_id":                   bson.ObjectIdHex(productID),
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&p)
	if err != nil {
		return nil, http.StatusNotFound, "product not found"
	}
	if requireType != "" && p.ProductType != requireType {
		return nil, http.StatusConflict, fmt.Sprintf("product is not %s", requireType)
	}
	return &p, 0, ""
}

// handleDownloadUploadURL issues a 15-min signed PUT URL the browser uses to
// upload directly to GCS. Path is tenant-scoped so a leaked URL still can't
// hit another tenant's namespace.
func handleDownloadUploadURL(c *gin.Context) {
	if downloadStorage == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage not configured"})
		return
	}
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	product, status, msg := loadOwnedProduct(tenantID, c.Param("id"), pkgmodels.ProductTypeDigitalDownload)
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

	objectPath := fmt.Sprintf("%s/downloads/%s/%s_%s",
		tenantID.Hex(),
		product.PublicId,
		utils.GeneratePublicId(),
		safeFileName(req.FileName),
	)
	signed, err := downloadStorage.GenerateUploadURL(downloadBucket, objectPath, req.ContentType)
	if err != nil {
		log.Printf("downloads: signed upload URL failed: %v", err)
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

// handleDownloadAttach finalizes a successful upload by creating the Asset
// row and appending its id to Product.Downloads.AssetIDs. Idempotent on the
// (product, object_path) pair — re-attaching the same path returns the
// existing asset.
func handleDownloadAttach(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	product, status, msg := loadOwnedProduct(tenantID, c.Param("id"), pkgmodels.ProductTypeDigitalDownload)
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}

	var req struct {
		FileName    string `json:"file_name" binding:"required"`
		ObjectPath  string `json:"object_path" binding:"required"`
		ContentType string `json:"content_type"`
		SizeBytes   int64  `json:"size_bytes"`
		Title       string `json:"title"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file_name and object_path are required"})
		return
	}

	publicURL := fmt.Sprintf("https://storage.googleapis.com/%s/%s", downloadBucket, req.ObjectPath)

	asset := pkgmodels.NewAsset()
	asset.TenantID = tenantID
	asset.Title = strings.TrimSpace(req.Title)
	if asset.Title == "" {
		asset.Title = req.FileName
	}
	asset.Kind = "download_file"
	asset.Status = "ready"
	asset.FileURL = publicURL
	asset.FileName = req.FileName
	asset.FileType = req.ContentType
	asset.FileSize = req.SizeBytes
	asset.S3Key = req.ObjectPath

	if err := db.GetCollection(pkgmodels.AssetCollection).Insert(asset); err != nil {
		log.Printf("downloads: insert asset failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save asset"})
		return
	}

	cfg := product.Downloads
	if cfg == nil {
		cfg = &pkgmodels.DigitalDownloadConfig{}
	}
	cfg.AssetIDs = append(cfg.AssetIDs, asset.Id)

	if err := db.GetCollection(pkgmodels.ProductCollection).Update(
		bson.M{"_id": product.Id, "tenant_id": tenantID},
		bson.M{"$set": bson.M{"downloads": cfg}},
	); err != nil {
		log.Printf("downloads: update product asset list failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to attach asset"})
		return
	}
	c.JSON(http.StatusCreated, asset)
}

// handleDownloadList returns the tenant-side listing of attached assets.
func handleDownloadList(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	product, status, msg := loadOwnedProduct(tenantID, c.Param("id"), pkgmodels.ProductTypeDigitalDownload)
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	assets := loadAssetsByIDs(tenantID, productAssetIDs(product))
	c.JSON(http.StatusOK, gin.H{
		"assets":           assets,
		"allow_zip_bundle": product.Downloads != nil && product.Downloads.AllowZipBundle,
	})
}

// handleDownloadSettings updates AllowZipBundle (the only product-level toggle
// for downloads at the moment). Asset list edits flow through attach/delete.
func handleDownloadSettings(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	product, status, msg := loadOwnedProduct(tenantID, c.Param("id"), pkgmodels.ProductTypeDigitalDownload)
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	var req struct {
		AllowZipBundle *bool `json:"allow_zip_bundle"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	cfg := product.Downloads
	if cfg == nil {
		cfg = &pkgmodels.DigitalDownloadConfig{}
	}
	if req.AllowZipBundle != nil {
		cfg.AllowZipBundle = *req.AllowZipBundle
	}
	if err := db.GetCollection(pkgmodels.ProductCollection).Update(
		bson.M{"_id": product.Id, "tenant_id": tenantID},
		bson.M{"$set": bson.M{"downloads": cfg}},
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update settings"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "settings updated"})
}

// handleDownloadDelete removes an asset id from the product's list and
// soft-deletes the Asset row. The GCS object is also removed when storage is
// configured; failure to delete the object is logged but not fatal because
// the entitlement layer no longer surfaces it.
func handleDownloadDelete(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	product, status, msg := loadOwnedProduct(tenantID, c.Param("id"), pkgmodels.ProductTypeDigitalDownload)
	if status != 0 {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	assetID := c.Param("assetId")
	if !bson.IsObjectIdHex(assetID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid asset id"})
		return
	}
	objID := bson.ObjectIdHex(assetID)

	var asset pkgmodels.Asset
	if err := db.GetCollection(pkgmodels.AssetCollection).Find(bson.M{
		"_id":       objID,
		"tenant_id": tenantID,
	}).One(&asset); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "asset not found"})
		return
	}

	cfg := product.Downloads
	if cfg != nil {
		filtered := cfg.AssetIDs[:0]
		for _, id := range cfg.AssetIDs {
			if id != objID {
				filtered = append(filtered, id)
			}
		}
		cfg.AssetIDs = filtered
	}
	if err := db.GetCollection(pkgmodels.ProductCollection).Update(
		bson.M{"_id": product.Id, "tenant_id": tenantID},
		bson.M{"$set": bson.M{"downloads": cfg}},
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to detach asset"})
		return
	}
	if err := db.GetCollection(pkgmodels.AssetCollection).Update(
		bson.M{"_id": objID, "tenant_id": tenantID},
		bson.M{"$currentDate": bson.M{"timestamps.deleted_at": true}},
	); err != nil {
		log.Printf("downloads: soft-delete asset failed: %v", err)
	}
	if downloadStorage != nil && asset.S3Key != "" {
		if err := downloadStorage.DeleteObject(downloadBucket, asset.S3Key); err != nil {
			log.Printf("downloads: GCS delete failed for %s: %v", asset.S3Key, err)
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "asset removed"})
}

// --- Customer side ---

// handleCustomerDownloadList returns the public asset metadata for a download
// product the customer is entitled to. URLs are NOT returned here — the
// customer must call /assets/:id/url separately to mint a 60s signed URL.
func handleCustomerDownloadList(c *gin.Context) {
	tenantID, contactID, ok := requireCustomer(c)
	if !ok {
		return
	}
	product, ok := loadEntitledDownloadProduct(c, tenantID, contactID, c.Param("productId"))
	if !ok {
		return
	}
	assets := loadAssetsByIDs(tenantID, productAssetIDs(product))

	type dto struct {
		ID       string `json:"id"`
		PublicID string `json:"public_id"`
		Title    string `json:"title"`
		FileName string `json:"file_name"`
		FileType string `json:"file_type"`
		FileSize int64  `json:"file_size"`
	}
	out := make([]dto, 0, len(assets))
	for _, a := range assets {
		out = append(out, dto{
			ID:       a.Id.Hex(),
			PublicID: a.PublicId,
			Title:    a.Title,
			FileName: a.FileName,
			FileType: a.FileType,
			FileSize: a.FileSize,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"product": gin.H{
			"id":            product.Id.Hex(),
			"public_id":     product.PublicId,
			"name":          product.Name,
			"description":   product.Description,
			"thumbnail_url": product.ThumbnailURL,
		},
		"assets":           out,
		"allow_zip_bundle": product.Downloads != nil && product.Downloads.AllowZipBundle,
	})
}

// handleCustomerDownloadURL mints a 60s signed GET URL for a single asset.
// The URL is returned in the JSON body; the frontend must immediately
// initiate the download as a hidden blob fetch so the URL never reaches the
// address bar. Re-clicking the button mints a fresh URL.
func handleCustomerDownloadURL(c *gin.Context) {
	if downloadStorage == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage not configured"})
		return
	}
	tenantID, contactID, ok := requireCustomer(c)
	if !ok {
		return
	}
	product, ok := loadEntitledDownloadProduct(c, tenantID, contactID, c.Param("productId"))
	if !ok {
		return
	}
	assetIDStr := c.Param("assetId")
	if !bson.IsObjectIdHex(assetIDStr) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid asset id"})
		return
	}
	assetID := bson.ObjectIdHex(assetIDStr)

	if !productOwnsAsset(product, assetID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "asset not part of product"})
		return
	}

	var asset pkgmodels.Asset
	if err := db.GetCollection(pkgmodels.AssetCollection).Find(bson.M{
		"_id":                   assetID,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&asset); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "asset not found"})
		return
	}
	if asset.S3Key == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "asset missing object path"})
		return
	}
	signed, err := downloadStorage.GenerateSignedDownloadURL(downloadBucket, asset.S3Key, 60*time.Second)
	if err != nil {
		log.Printf("downloads: sign GET failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to issue download URL"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"url":        signed,
		"file_name":  asset.FileName,
		"file_type":  asset.FileType,
		"file_size":  asset.FileSize,
		"expires_in": 60,
	})
}

// handleCustomerDownloadZip streams a zip of every asset on a product when
// AllowZipBundle is enabled. Each entry is fetched server-side via signed
// URL, so the customer's browser never sees the underlying GCS paths. This
// is heavier than per-file downloads — only used for explicit "Download All"
// clicks.
func handleCustomerDownloadZip(c *gin.Context) {
	if downloadStorage == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage not configured"})
		return
	}
	tenantID, contactID, ok := requireCustomer(c)
	if !ok {
		return
	}
	product, ok := loadEntitledDownloadProduct(c, tenantID, contactID, c.Param("productId"))
	if !ok {
		return
	}
	if product.Downloads == nil || !product.Downloads.AllowZipBundle {
		c.JSON(http.StatusForbidden, gin.H{"error": "zip bundle not enabled"})
		return
	}
	assets := loadAssetsByIDs(tenantID, productAssetIDs(product))
	if len(assets) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "no assets to bundle"})
		return
	}

	zipName := fmt.Sprintf("%s.zip", safeFileName(product.Name))
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", zipName))

	zw := zip.NewWriter(c.Writer)
	defer zw.Close()

	for _, a := range assets {
		if a.S3Key == "" {
			continue
		}
		signed, err := downloadStorage.GenerateSignedDownloadURL(downloadBucket, a.S3Key, 5*time.Minute)
		if err != nil {
			log.Printf("downloads: zip sign failed for %s: %v", a.S3Key, err)
			continue
		}
		resp, err := http.Get(signed)
		if err != nil {
			log.Printf("downloads: zip fetch failed for %s: %v", a.S3Key, err)
			continue
		}
		entry, err := zw.Create(safeFileName(a.FileName))
		if err != nil {
			resp.Body.Close()
			log.Printf("downloads: zip entry create failed: %v", err)
			continue
		}
		if _, err := io.Copy(entry, resp.Body); err != nil {
			log.Printf("downloads: zip stream failed for %s: %v", a.S3Key, err)
		}
		resp.Body.Close()
	}
}

// --- Helpers ---

func productAssetIDs(p *pkgmodels.Product) []bson.ObjectId {
	if p == nil || p.Downloads == nil {
		return nil
	}
	return p.Downloads.AssetIDs
}

func productOwnsAsset(p *pkgmodels.Product, id bson.ObjectId) bool {
	for _, x := range productAssetIDs(p) {
		if x == id {
			return true
		}
	}
	return false
}

func loadAssetsByIDs(tenantID bson.ObjectId, ids []bson.ObjectId) []pkgmodels.Asset {
	if len(ids) == 0 {
		return nil
	}
	var assets []pkgmodels.Asset
	err := db.GetCollection(pkgmodels.AssetCollection).Find(bson.M{
		"_id":                   bson.M{"$in": ids},
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).All(&assets)
	if err != nil {
		log.Printf("downloads: load assets failed: %v", err)
		return nil
	}
	// Preserve the order the tenant chose in Product.Downloads.AssetIDs.
	pos := make(map[bson.ObjectId]int, len(ids))
	for i, id := range ids {
		pos[id] = i
	}
	ordered := make([]pkgmodels.Asset, len(assets))
	for i := range assets {
		ordered[pos[assets[i].Id]] = assets[i]
	}
	return ordered
}

// requireCustomer extracts and validates the customer-auth pair from the
// request. Writes the appropriate error response and returns ok=false on
// failure so handlers can early-return without duplicating the boilerplate.
func requireCustomer(c *gin.Context) (bson.ObjectId, bson.ObjectId, bool) {
	contactStr := auth.GetContactID(c)
	tenantStr := auth.GetTenantID(c)
	if contactStr == "" || tenantStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return "", "", false
	}
	if !bson.IsObjectIdHex(contactStr) || !bson.IsObjectIdHex(tenantStr) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid token data"})
		return "", "", false
	}
	return bson.ObjectIdHex(tenantStr), bson.ObjectIdHex(contactStr), true
}

// loadEntitledDownloadProduct verifies the customer holds an active offer that
// includes the requested digital_download product. Reuses the badge-grant
// resolution from handleGetLibraryProducts so the entitlement contract is
// consistent across the library.
func loadEntitledDownloadProduct(c *gin.Context, tenantID, contactID bson.ObjectId, productID string) (*pkgmodels.Product, bool) {
	if !bson.IsObjectIdHex(productID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
		return nil, false
	}
	pid := bson.ObjectIdHex(productID)
	var product pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"_id":                   pid,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&product); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return nil, false
	}
	if product.ProductType != pkgmodels.ProductTypeDigitalDownload {
		c.JSON(http.StatusConflict, gin.H{"error": "product is not a digital download"})
		return nil, false
	}
	if err := assertContactEntitled(tenantID, contactID, pid); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return nil, false
	}
	return &product, true
}

// assertContactEntitled confirms the contact owns at least one active offer
// that includes productID. Returns a sentinel error otherwise.
func assertContactEntitled(tenantID, contactID, productID bson.ObjectId) error {
	var contact pkgmodels.User
	if err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
		"_id":       contactID,
		"tenant_id": tenantID,
	}).One(&contact); err != nil {
		return errors.New("contact not found")
	}
	if len(contact.Badges) == 0 {
		return errors.New("no entitlements")
	}
	var badgeNames []string
	for _, badgeID := range contact.Badges {
		var b pkgmodels.Badge
		if err := db.GetCollection(pkgmodels.BadgeCollection).FindId(badgeID).One(&b); err == nil {
			badgeNames = append(badgeNames, b.Name)
		}
	}
	if len(badgeNames) == 0 {
		return errors.New("no entitlements")
	}
	count, err := db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"granted_badges":        bson.M{"$in": badgeNames},
		"included_products":     productID,
		"timestamps.deleted_at": nil,
	}).Count()
	if err != nil {
		return errors.New("entitlement check failed")
	}
	if count == 0 {
		return errors.New("not entitled to this product")
	}
	return nil
}

// safeFileName scrubs a name for use as a GCS object path component or zip
// entry. Keeps alphanumerics, dots, hyphens, and underscores; everything
// else collapses to a single underscore.
func safeFileName(name string) string {
	name = path.Base(name)
	if name == "" || name == "." || name == "/" {
		return "file"
	}
	var b strings.Builder
	prevUnderscore := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
			prevUnderscore = false
		default:
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	out := strings.Trim(b.String(), "_.")
	if out == "" {
		return "file"
	}
	return out
}
