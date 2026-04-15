package site

import (
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// CreateSitePageVersion inserts a new version.
func CreateSitePageVersion(v *SitePageVersion) error {
	return db.GetCollection(pkgmodels.SitePageVersionCollection).Insert(v)
}

// ListVersionsByPage returns all versions for a page, newest first.
func ListVersionsByPage(pageID, tenantID bson.ObjectId) ([]SitePageVersion, error) {
	var versions []SitePageVersion
	err := db.GetCollection(pkgmodels.SitePageVersionCollection).Find(bson.M{
		"site_page_id":          pageID,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).Sort("-version_number").All(&versions)
	return versions, err
}

// GetVersionByID retrieves a specific version.
func GetVersionByID(id, tenantID bson.ObjectId) (*SitePageVersion, error) {
	var version SitePageVersion
	err := db.GetCollection(pkgmodels.SitePageVersionCollection).Find(bson.M{
		"_id":                   id,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&version)
	if err != nil {
		return nil, err
	}
	return &version, nil
}

// GetLatestVersionNumber returns the current max version number for a page.
func GetLatestVersionNumber(pageID, tenantID bson.ObjectId) (int, error) {
	var version SitePageVersion
	err := db.GetCollection(pkgmodels.SitePageVersionCollection).Find(bson.M{
		"site_page_id":          pageID,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).Sort("-version_number").One(&version)
	if err != nil {
		return 0, nil // No versions yet
	}
	return version.VersionNumber, nil
}
