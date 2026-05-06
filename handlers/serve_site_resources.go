package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterSiteResourceRoutes registers lightweight resource proxy endpoints
// used by the website builder UI to populate dynamic select/picker fields.
func RegisterSiteResourceRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.GET("/sites/resources/:resourceType", handleListResources)
}

// resourceItem is a lightweight resource reference for builder dropdowns.
type resourceItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func handleListResources(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	resourceType := c.Param("resourceType")

	switch resourceType {
	case "offers":
		items, err := listTenantOffers(tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list offers"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "resources": items})

	case "products":
		items, err := listTenantProducts(tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list products"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "resources": items})

	case "funnels":
		items, err := listTenantFunnels(tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list funnels"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "resources": items})

	case "domains":
		items, err := listTenantDomains(tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list domains"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "resources": items})

	case "courses":
		items, err := listTenantCourses(tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list courses"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "resources": items})

	case "forms":
		items, err := listTenantForms(tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list forms"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "resources": items})

	case "downloads":
		items, err := listTenantDigitalDownloads(tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list downloads"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "resources": items})

	case "badges":
		items, err := listTenantBadgeRefs(tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list badges"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "resources": items})

	case "storylines", "stories":
		items, err := listTenantStories(tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list stories"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "resources": items})

	case "email_lists", "lists":
		items, err := listTenantEmailLists(tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list email lists"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "resources": items})

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown resource type: " + resourceType})
	}
}

func listTenantOffers(tenantID bson.ObjectId) ([]resourceItem, error) {
	var offers []pkgmodels.Offer
	err := db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).Select(bson.M{"_id": 1, "title": 1}).All(&offers)
	if err != nil {
		return nil, err
	}
	items := make([]resourceItem, 0, len(offers))
	for _, o := range offers {
		items = append(items, resourceItem{ID: o.Id.Hex(), Name: o.Title})
	}
	return items, nil
}

func listTenantProducts(tenantID bson.ObjectId) ([]resourceItem, error) {
	var products []pkgmodels.Product
	err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).Select(bson.M{"_id": 1, "name": 1}).All(&products)
	if err != nil {
		return nil, err
	}
	items := make([]resourceItem, 0, len(products))
	for _, p := range products {
		items = append(items, resourceItem{ID: p.Id.Hex(), Name: p.Name})
	}
	return items, nil
}

func listTenantFunnels(tenantID bson.ObjectId) ([]resourceItem, error) {
	var funnels []pkgmodels.Funnel
	err := db.GetCollection(pkgmodels.FunnelCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).Select(bson.M{"_id": 1, "name": 1}).All(&funnels)
	if err != nil {
		return nil, err
	}
	items := make([]resourceItem, 0, len(funnels))
	for _, f := range funnels {
		items = append(items, resourceItem{ID: f.Id.Hex(), Name: f.Name})
	}
	return items, nil
}

func listTenantCourses(tenantID bson.ObjectId) ([]resourceItem, error) {
	var courses []pkgmodels.Product
	err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"product_type":          "course",
		"status":                "active",
		"timestamps.deleted_at": nil,
	}).Select(bson.M{"_id": 1, "name": 1}).All(&courses)
	if err != nil {
		return nil, err
	}
	items := make([]resourceItem, 0, len(courses))
	for _, c := range courses {
		items = append(items, resourceItem{ID: c.Id.Hex(), Name: c.Name})
	}
	return items, nil
}

func listTenantForms(tenantID bson.ObjectId) ([]resourceItem, error) {
	var forms []pkgmodels.PageForm
	err := db.GetCollection(pkgmodels.PageFormCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).Select(bson.M{"public_id": 1, "name": 1}).All(&forms)
	if err != nil {
		return nil, err
	}
	items := make([]resourceItem, 0, len(forms))
	for _, f := range forms {
		// Builder picker uses public_id (not ObjectId hex) so the public
		// submit endpoint can resolve the form without tenant auth.
		items = append(items, resourceItem{ID: f.PublicId, Name: f.Name})
	}
	return items, nil
}

func listTenantDigitalDownloads(tenantID bson.ObjectId) ([]resourceItem, error) {
	var products []pkgmodels.Product
	err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"product_type":          pkgmodels.ProductTypeDigitalDownload,
		"timestamps.deleted_at": nil,
	}).Select(bson.M{"_id": 1, "name": 1}).All(&products)
	if err != nil {
		return nil, err
	}
	items := make([]resourceItem, 0, len(products))
	for _, p := range products {
		items = append(items, resourceItem{ID: p.Id.Hex(), Name: p.Name})
	}
	return items, nil
}

// tenantScope returns an $or query matching either tenant_id (ObjectId) or
// subscriber_id (hex string). Different collections were created at different
// stages of the schema's evolution and use one or the other (sometimes both).
// Using $or makes the resource-list handlers tolerant of either shape.
func tenantScope(tenantID bson.ObjectId) bson.M {
	return bson.M{
		"$or": []bson.M{
			{"tenant_id": tenantID},
			{"subscriber_id": tenantID.Hex()},
		},
	}
}

func listTenantBadgeRefs(tenantID bson.ObjectId) ([]resourceItem, error) {
	q := tenantScope(tenantID)
	q["timestamps.deleted_at"] = nil
	var badges []pkgmodels.Badge
	err := db.GetCollection(pkgmodels.BadgeCollection).Find(q).Select(bson.M{"public_id": 1, "name": 1}).All(&badges)
	if err != nil {
		return nil, err
	}
	items := make([]resourceItem, 0, len(badges))
	for _, b := range badges {
		// Builder uses public_id so the picker value can be passed straight
		// through to the public form-submit / action-chain endpoints, which
		// expect public ids (see Phase 2 FormOnSubmit shape).
		items = append(items, resourceItem{ID: b.PublicId, Name: b.Name})
	}
	return items, nil
}

func listTenantStories(tenantID bson.ObjectId) ([]resourceItem, error) {
	q := tenantScope(tenantID)
	q["timestamps.deleted_at"] = nil
	var stories []pkgmodels.Story
	err := db.GetCollection(pkgmodels.StoryCollection).Find(q).Select(bson.M{"public_id": 1, "name": 1}).All(&stories)
	if err != nil {
		return nil, err
	}
	items := make([]resourceItem, 0, len(stories))
	for _, s := range stories {
		items = append(items, resourceItem{ID: s.PublicId, Name: s.Name})
	}
	return items, nil
}

func listTenantEmailLists(tenantID bson.ObjectId) ([]resourceItem, error) {
	// EmailList stores subscriber_id only (no tenant_id field on the struct),
	// so we use the same scope helper which matches either field.
	q := tenantScope(tenantID)
	q["timestamps.deleted_at"] = nil
	var lists []pkgmodels.EmailList
	err := db.GetCollection(pkgmodels.EmailListCollection).Find(q).Select(bson.M{"public_id": 1, "name": 1}).All(&lists)
	if err != nil {
		return nil, err
	}
	items := make([]resourceItem, 0, len(lists))
	for _, l := range lists {
		items = append(items, resourceItem{ID: l.PublicId, Name: l.Name})
	}
	return items, nil
}

func listTenantDomains(tenantID bson.ObjectId) ([]resourceItem, error) {
	var domains []pkgmodels.TenantDomain
	err := db.GetCollection(pkgmodels.DomainCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).Select(bson.M{"_id": 1, "hostname": 1, "is_verified": 1}).All(&domains)
	if err != nil {
		return nil, err
	}
	items := make([]resourceItem, 0, len(domains))
	for _, d := range domains {
		label := d.Hostname
		if d.IsVerified {
			label += " ✓"
		}
		items = append(items, resourceItem{ID: d.Id.Hex(), Name: label})
	}
	return items, nil
}
