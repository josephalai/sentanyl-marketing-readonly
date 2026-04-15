package site

import (
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// CreateSitePage inserts a new SitePage.
func CreateSitePage(p *SitePage) error {
	return db.GetCollection(pkgmodels.SitePageCollection).Insert(p)
}

// GetSitePageByID fetches a page by ObjectId scoped to tenant.
func GetSitePageByID(id, tenantID bson.ObjectId) (*SitePage, error) {
	var page SitePage
	err := db.GetCollection(pkgmodels.SitePageCollection).Find(bson.M{
		"_id":                   id,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&page)
	if err != nil {
		return nil, err
	}
	return &page, nil
}

// ListPagesBySite returns all non-deleted pages for a site.
func ListPagesBySite(siteID, tenantID bson.ObjectId) ([]SitePage, error) {
	var pages []SitePage
	err := db.GetCollection(pkgmodels.SitePageCollection).Find(bson.M{
		"site_id":               siteID,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).All(&pages)
	return pages, err
}

// UpdateSitePage applies partial updates to a page.
func UpdateSitePage(id, tenantID bson.ObjectId, updates bson.M) error {
	now := time.Now()
	updates["timestamps.updated_at"] = now
	return db.GetCollection(pkgmodels.SitePageCollection).Update(
		bson.M{"_id": id, "tenant_id": tenantID},
		bson.M{"$set": updates},
	)
}

// SoftDeleteSitePage sets deleted_at on a page.
func SoftDeleteSitePage(id, tenantID bson.ObjectId) error {
	now := time.Now()
	return db.GetCollection(pkgmodels.SitePageCollection).Update(
		bson.M{"_id": id, "tenant_id": tenantID},
		bson.M{"$set": bson.M{"timestamps.deleted_at": now}},
	)
}

// FindPublishedPageBySlug finds a published page by slug for a site.
func FindPublishedPageBySlug(siteID bson.ObjectId, slug string) (*SitePage, error) {
	var page SitePage
	err := db.GetCollection(pkgmodels.SitePageCollection).Find(bson.M{
		"site_id":               siteID,
		"slug":                  slug,
		"status":                "published",
		"timestamps.deleted_at": nil,
	}).One(&page)
	if err != nil {
		return nil, err
	}
	return &page, nil
}

// FindHomePageForSite returns the home page for a published site.
func FindHomePageForSite(siteID bson.ObjectId) (*SitePage, error) {
	var page SitePage
	err := db.GetCollection(pkgmodels.SitePageCollection).Find(bson.M{
		"site_id":               siteID,
		"is_home":               true,
		"timestamps.deleted_at": nil,
	}).One(&page)
	if err != nil {
		return nil, err
	}
	return &page, nil
}
