// Command backfill-access-grants reconciles the AccessGrant ledger for
// historical customers (COM-CC-011). It creates grants two ways, idempotently:
//
//  1. For every provisioned/pending PurchaseItem that has no grant yet (the
//     forward ledger path), a grant sourced "purchase".
//  2. For contacts whose entitlement predates the ledger — resolved from their
//     badges → Offers(granted_badges) → included products — a grant per product
//     sourced "migration", only when the contact has no grant for that product.
//
// Run it before flipping ACCESS_GRANTS_ONLY=1 so grants become the sole library
// authority without dropping any existing customer's access. Safe to re-run.
//
// Usage:
//
//	go run ./marketing-service/cmd/backfill-access-grants            # dry-run
//	go run ./marketing-service/cmd/backfill-access-grants -apply     # write
package main

import (
	"flag"
	"log"
	"os"

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
	db.MongoDefaultCollectionName = "users"
	db.UsingLocalMongo = true
	db.InitMongoConnection()

	grantCol := db.GetCollection(pkgmodels.AccessGrantCollection)
	created := 0

	// 1. Forward: PurchaseItems lacking a grant.
	var items []pkgmodels.PurchaseItem
	_ = db.GetCollection(pkgmodels.PurchaseItemCollection).Find(bson.M{}).All(&items)
	for i := range items {
		it := &items[i]
		n, _ := grantCol.Find(bson.M{"purchase_item_id": it.Id}).Count()
		if n > 0 {
			continue
		}
		log.Printf("[purchase] grant for contact=%s product=%s (item=%s)", it.ContactID.Hex(), it.ProductID.Hex(), it.Id.Hex())
		if apply {
			g := pkgmodels.NewAccessGrant(it.TenantID, it.ContactID, it.ProductID, it.Id, it.OfferID, "purchase")
			if err := grantCol.Insert(g); err != nil {
				log.Printf("  insert failed: %v", err)
				continue
			}
		}
		created++
	}

	// 2. Migration: badge-derived entitlement for pre-ledger contacts.
	var contacts []pkgmodels.User
	_ = db.GetCollection(pkgmodels.UserCollection).Find(bson.M{"badges": bson.M{"$exists": true, "$ne": []interface{}{}}}).All(&contacts)
	for ci := range contacts {
		contact := &contacts[ci]
		if contact.TenantID == "" || len(contact.Badges) == 0 {
			continue
		}
		var badgeNames []string
		for _, bID := range contact.Badges {
			var b pkgmodels.Badge
			if err := db.GetCollection(pkgmodels.BadgeCollection).FindId(bID).One(&b); err == nil {
				badgeNames = append(badgeNames, b.Name)
			}
		}
		if len(badgeNames) == 0 {
			continue
		}
		var offers []pkgmodels.Offer
		db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
			"tenant_id":      contact.TenantID,
			"granted_badges": bson.M{"$in": badgeNames},
		}).All(&offers)
		for _, offer := range offers {
			for _, pid := range offer.IncludedProducts {
				n, _ := grantCol.Find(bson.M{"tenant_id": contact.TenantID, "contact_id": contact.Id, "product_id": pid}).Count()
				if n > 0 {
					continue
				}
				log.Printf("[migration] grant for contact=%s product=%s (offer=%s)", contact.Id.Hex(), pid.Hex(), offer.Id.Hex())
				if apply {
					g := pkgmodels.NewAccessGrant(contact.TenantID, contact.Id, pid, "", offer.Id, "migration")
					if err := grantCol.Insert(g); err != nil {
						log.Printf("  insert failed: %v", err)
						continue
					}
				}
				created++
			}
		}
	}

	mode := "DRY-RUN (no writes)"
	if apply {
		mode = "APPLIED"
	}
	log.Printf("backfill-access-grants %s: %d grants %s", mode, created, ternary(apply, "created", "would be created"))
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
