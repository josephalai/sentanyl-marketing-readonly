package site

import (
	"log"

	"gopkg.in/mgo.v2"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// EnsureIndexes creates production-relevant MongoDB indexes for the website
// builder collections. Safe to call at startup — mgo's EnsureIndex is
// idempotent.
func EnsureIndexes() {
	ensureSiteIndexes()
	ensureSitePageIndexes()
	ensureSitePageVersionIndexes()
	log.Println("site: MongoDB indexes ensured")
}

func ensureSiteIndexes() {
	col := db.GetCollection(pkgmodels.SiteCollection)

	indexes := []mgo.Index{
		{
			Key:        []string{"tenant_id"},
			Background: true,
		},
		{
			Key:        []string{"public_id"},
			Background: true,
		},
		{
			Key:        []string{"attached_domains"},
			Background: true,
		},
		{
			Key:        []string{"status", "timestamps.deleted_at"},
			Background: true,
		},
	}

	for _, idx := range indexes {
		if err := col.EnsureIndex(idx); err != nil {
			log.Printf("site: failed to ensure index %v on sites: %v", idx.Key, err)
		}
	}
}

func ensureSitePageIndexes() {
	col := db.GetCollection(pkgmodels.SitePageCollection)

	indexes := []mgo.Index{
		{
			Key:        []string{"site_id", "tenant_id"},
			Background: true,
		},
		{
			Key:        []string{"site_id", "slug"},
			Background: true,
		},
		{
			Key:        []string{"site_id", "is_home"},
			Background: true,
		},
		{
			Key:        []string{"tenant_id"},
			Background: true,
		},
	}

	for _, idx := range indexes {
		if err := col.EnsureIndex(idx); err != nil {
			log.Printf("site: failed to ensure index %v on site_pages: %v", idx.Key, err)
		}
	}
}

func ensureSitePageVersionIndexes() {
	col := db.GetCollection(pkgmodels.SitePageVersionCollection)

	indexes := []mgo.Index{
		{
			Key:        []string{"site_page_id", "tenant_id", "-version_number"},
			Background: true,
		},
		{
			Key:        []string{"site_id", "tenant_id"},
			Background: true,
		},
		{
			Key:        []string{"tenant_id"},
			Background: true,
		},
	}

	for _, idx := range indexes {
		if err := col.EnsureIndex(idx); err != nil {
			log.Printf("site: failed to ensure index %v on site_page_versions: %v", idx.Key, err)
		}
	}
}
