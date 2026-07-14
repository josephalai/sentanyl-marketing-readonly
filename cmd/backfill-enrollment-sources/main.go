// Command backfill-enrollment-sources stamps commercial provenance onto
// historical CourseEnrollment rows (DEL-007/008): each enrollment lacking a
// purchase_item_id is matched to the oldest un-claimed PurchaseItem for the
// same (tenant, contact, product) and gains purchase_item_id + offer_id +
// source "purchase". Enrollments with no matching item (form grants, manual,
// pre-ledger history) are stamped source "legacy" so reporting can tell the
// difference from an unprocessed row.
//
// Idempotent and race-free against the sparse unique purchase_item_id index:
// a claimed item is never assigned twice. Dry-run by default.
//
// Rollback: the stamped fields are additive; unset purchase_item_id/offer_id/
// source on course_enrollments to revert (no reads depend on them for
// authorization — entitlement is the Access Grant ledger).
//
// Usage:
//
//	go run ./marketing-service/cmd/backfill-enrollment-sources          # dry-run
//	go run ./marketing-service/cmd/backfill-enrollment-sources -apply   # write
package main

import (
	"flag"
	"log"
	"os"

	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

func main() {
	var host, port, dbName string
	var apply bool
	flag.StringVar(&host, "mongo-host", envOr("MONGO_HOST", "localhost"), "Mongo host")
	flag.StringVar(&port, "mongo-port", envOr("MONGO_PORT", "27017"), "Mongo port")
	flag.StringVar(&dbName, "mongo-db", envOr("MONGO_DB", "sentanyl_db"), "Mongo database name")
	flag.BoolVar(&apply, "apply", false, "Write changes (default is dry-run)")
	flag.Parse()

	db.MongoHost = host
	db.MongoPort = port
	db.MongoDB = dbName
	db.UsingLocalMongo = true
	db.InitMongoConnection()

	enrollments := db.GetCollection(pkgmodels.CourseEnrollmentCollection)
	items := db.GetCollection(pkgmodels.PurchaseItemCollection)

	var rows []pkgmodels.CourseEnrollment
	if err := enrollments.Find(bson.M{
		"purchase_item_id": bson.M{"$exists": false},
	}).All(&rows); err != nil {
		log.Fatalf("list enrollments: %v", err)
	}

	claimed := map[bson.ObjectId]bool{} // items assigned during this run
	matched, legacy := 0, 0
	for _, e := range rows {
		item := findItem(items, e, claimed)
		if item == nil {
			legacy++
			if apply {
				if err := enrollments.UpdateId(e.Id, bson.M{"$set": bson.M{"source": "legacy"}}); err != nil {
					log.Printf("stamp legacy %s: %v", e.Id.Hex(), err)
				}
			}
			continue
		}
		matched++
		claimed[item.Id] = true
		if apply {
			if err := enrollments.UpdateId(e.Id, bson.M{"$set": bson.M{
				"purchase_item_id": item.Id,
				"offer_id":         item.OfferID,
				"source":           "purchase",
			}}); err != nil {
				log.Printf("stamp %s: %v", e.Id.Hex(), err)
			}
		}
	}

	mode := "DRY-RUN"
	if apply {
		mode = "APPLIED"
	}
	log.Printf("[%s] course enrollments scanned=%d matched-to-purchase-item=%d legacy=%d",
		mode, len(rows), matched, legacy)
}

// findItem returns the oldest purchase item for the enrollment's
// (tenant, contact, product) not already claimed by another enrollment —
// neither in the DB (an enrollment already carries it) nor in this run.
func findItem(items *mgo.Collection, e pkgmodels.CourseEnrollment, claimed map[bson.ObjectId]bool) *pkgmodels.PurchaseItem {
	var candidates []pkgmodels.PurchaseItem
	_ = items.Find(bson.M{
		"tenant_id":  e.TenantID,
		"contact_id": e.ContactID,
		"product_id": e.ProductID,
	}).Sort("_id").All(&candidates)
	enrollCol := db.GetCollection(pkgmodels.CourseEnrollmentCollection)
	for i := range candidates {
		it := &candidates[i]
		if claimed[it.Id] {
			continue
		}
		// Skip items already stamped onto another enrollment.
		if n, _ := enrollCol.Find(bson.M{"purchase_item_id": it.Id}).Count(); n > 0 {
			continue
		}
		return it
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
