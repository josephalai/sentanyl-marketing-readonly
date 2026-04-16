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
