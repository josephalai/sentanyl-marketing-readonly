package channel

import (
	"log"

	"gopkg.in/mgo.v2"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// EnsureIndexes creates MongoDB indexes for the frontend_channels collection.
// Safe to call at startup — mgo's EnsureIndex is idempotent.
func EnsureIndexes() {
	col := db.GetCollection(pkgmodels.FrontendChannelCollection)

	indexes := []mgo.Index{
		{
			Key:        []string{"tenant_id"},
			Background: true,
		},
		{
			Key:        []string{"public_id"},
			Unique:     true,
			Background: true,
		},
		{
			Key:        []string{"domain", "status", "timestamps.deleted_at"},
			Background: true,
		},
		{
			Key:        []string{"public_key"},
			Sparse:     true,
			Background: true,
		},
		{
			Key:        []string{"type", "status"},
			Background: true,
		},
	}

	for _, idx := range indexes {
		if err := col.EnsureIndex(idx); err != nil {
			log.Printf("channel: failed to ensure index %v on frontend_channels: %v", idx.Key, err)
		}
	}
	log.Println("channel: MongoDB indexes ensured")
}
