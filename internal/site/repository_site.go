package site

import (
	"regexp"
	"strings"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// CreateSite inserts a new Site into MongoDB.
func CreateSite(s *pkgmodels.Site) error {
	if s.Id == "" {
		s.Id = bson.NewObjectId()
	}
	if s.PublicId == "" {
		s.PublicId = utils.GeneratePublicId()
	}
	now := time.Now()
	s.SoftDeletes.CreatedAt = &now
	if s.Status == "" {
		s.Status = pkgmodels.SiteStatusDraft
	}
	return db.GetCollection(pkgmodels.SiteCollection).Insert(s)
}

// GetSiteByID fetches a site by its ObjectId and tenantID.
func GetSiteByID(id, tenantID bson.ObjectId) (*pkgmodels.Site, error) {
	var site pkgmodels.Site
	err := db.GetCollection(pkgmodels.SiteCollection).Find(bson.M{
		"_id":                   id,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&site)
	if err != nil {
		return nil, err
	}
	return &site, nil
}

// GetSiteByPublicID fetches a site by its public_id and tenantID.
func GetSiteByPublicID(publicID string, tenantID bson.ObjectId) (*pkgmodels.Site, error) {
	var site pkgmodels.Site
	err := db.GetCollection(pkgmodels.SiteCollection).Find(bson.M{
		"public_id":             publicID,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&site)
	if err != nil {
		return nil, err
	}
	return &site, nil
}

// ListSitesByTenant returns all non-deleted sites for a tenant.
func ListSitesByTenant(tenantID bson.ObjectId) ([]pkgmodels.Site, error) {
	var sites []pkgmodels.Site
	err := db.GetCollection(pkgmodels.SiteCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).All(&sites)
	return sites, err
}

// UpdateSite applies partial updates to a site.
func UpdateSite(id, tenantID bson.ObjectId, updates bson.M) error {
	now := time.Now()
	updates["timestamps.updated_at"] = now
	return db.GetCollection(pkgmodels.SiteCollection).Update(
		bson.M{"_id": id, "tenant_id": tenantID},
		bson.M{"$set": updates},
	)
}

// SoftDeleteSite sets the deleted_at timestamp.
func SoftDeleteSite(id, tenantID bson.ObjectId) error {
	now := time.Now()
	return db.GetCollection(pkgmodels.SiteCollection).Update(
		bson.M{"_id": id, "tenant_id": tenantID},
		bson.M{"$set": bson.M{"timestamps.deleted_at": now}},
	)
}

// FindSiteByDomain finds a published site matching a domain.
// Supports both attached custom domains and the dev host pattern
// ({public_id}.site.lvh.me).
//
// Hostname lookups are case-insensitive per RFC 1035: browsers lowercase the
// Host header when constructing the URL, so we match public_id with a
// case-insensitive regex anchored at both ends.
func FindSiteByDomain(domain string) (*pkgmodels.Site, error) {
	domain = strings.ToLower(domain)

	// Try dev host pattern first: {public_id}.site.lvh.me
	if strings.HasSuffix(domain, ".site.lvh.me") {
		publicID := strings.TrimSuffix(domain, ".site.lvh.me")
		if publicID != "" {
			var site pkgmodels.Site
			err := db.GetCollection(pkgmodels.SiteCollection).Find(bson.M{
				"public_id":             bson.RegEx{Pattern: "^" + regexp.QuoteMeta(publicID) + "$", Options: "i"},
				"status":                "published",
				"timestamps.deleted_at": nil,
			}).One(&site)
			if err == nil {
				return &site, nil
			}
		}
	}

	// Fall back to attached custom domains (case-insensitive match).
	var site pkgmodels.Site
	err := db.GetCollection(pkgmodels.SiteCollection).Find(bson.M{
		"attached_domains":      bson.RegEx{Pattern: "^" + regexp.QuoteMeta(domain) + "$", Options: "i"},
		"status":                "published",
		"timestamps.deleted_at": nil,
	}).One(&site)
	if err != nil {
		return nil, err
	}
	return &site, nil
}
