// Command reconcile-recurring-agreements separates legacy mixed Subscription
// rows into Purchase order truth and RecurringAgreement billing state.
// Dry-run is the default; pass -apply to write.
package main

import (
	"flag"
	"log"
	"os"
	"time"

	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

func main() {
	var host, port, dbName string
	var tenantHex string
	var apply bool
	flag.StringVar(&host, "mongo-host", envOr("MONGO_HOST", "localhost"), "Mongo host")
	flag.StringVar(&port, "mongo-port", envOr("MONGO_PORT", "27017"), "Mongo port")
	flag.StringVar(&dbName, "mongo-db", envOr("MONGO_DB", "sentanyl_db"), "Mongo database")
	flag.BoolVar(&apply, "apply", false, "Write changes (default dry-run)")
	flag.StringVar(&tenantHex, "tenant", "", "Restrict reconciliation to one tenant ObjectID")
	flag.Parse()
	db.MongoHost, db.MongoPort, db.MongoDB = host, port, dbName
	db.MongoDefaultCollectionName, db.UsingLocalMongo = "subscriptions", true
	db.InitMongoConnection()

	query := bson.M{"timestamps.deleted_at": nil}
	if tenantHex != "" {
		if !bson.IsObjectIdHex(tenantHex) {
			log.Fatal("invalid -tenant ObjectID")
		}
		query["tenant_id"] = bson.ObjectIdHex(tenantHex)
	}
	var legacy []pkgmodels.Subscription
	if err := db.GetCollection(pkgmodels.SubscriptionCollection).Find(query).All(&legacy); err != nil {
		log.Fatal(err)
	}
	created, linked, retired, conflicts := 0, 0, 0, 0
	for _, row := range legacy {
		var purchase pkgmodels.Purchase
		q := bson.M{"tenant_id": row.TenantID, "contact_id": row.ContactID, "offer_snapshot.offer_id": row.OfferID}
		if row.StripeSessionID != "" {
			q["stripe_session_id"] = row.StripeSessionID
		}
		if err := db.GetCollection(pkgmodels.PurchaseCollection).Find(q).Sort("-timestamps.created_at").One(&purchase); err != nil {
			log.Printf("CONFLICT legacy=%s has no matching Purchase", row.Id.Hex())
			conflicts++
			continue
		}
		if row.StripeSubscriptionID == "" {
			log.Printf("retire one-time legacy=%s purchase=%s", row.Id.Hex(), purchase.Id.Hex())
			if apply {
				now := time.Now()
				_ = db.GetCollection(pkgmodels.SubscriptionCollection).UpdateId(row.Id, bson.M{"$set": bson.M{"status": "retired", "timestamps.updated_at": now, "timestamps.deleted_at": now}})
			}
			retired++
			continue
		}
		var agreement pkgmodels.RecurringAgreement
		aq := bson.M{"tenant_id": row.TenantID, "stripe_subscription_id": row.StripeSubscriptionID}
		err := db.GetCollection(pkgmodels.RecurringAgreementCollection).Find(aq).One(&agreement)
		if err != nil && err != mgo.ErrNotFound {
			log.Printf("CONFLICT legacy=%s agreement lookup: %v", row.Id.Hex(), err)
			conflicts++
			continue
		}
		if err == mgo.ErrNotFound {
			agreement = *pkgmodels.NewRecurringAgreement(row.TenantID, row.ContactID, row.OfferID, purchase.Id, row.StripeSubscriptionID)
			agreement.Status = row.Status
			log.Printf("create recurring agreement=%s legacy=%s", agreement.Id.Hex(), row.Id.Hex())
			if apply {
				if err := db.GetCollection(pkgmodels.RecurringAgreementCollection).Insert(&agreement); err != nil && !mgo.IsDup(err) {
					log.Printf("CONFLICT legacy=%s insert: %v", row.Id.Hex(), err)
					conflicts++
					continue
				}
			}
			created++
		}
		if apply {
			_ = db.GetCollection(pkgmodels.SubscriptionCollection).UpdateId(row.Id, bson.M{"$set": bson.M{"migrated_to_agreement_id": agreement.Id, "status": "migrated", "timestamps.updated_at": time.Now()}})
		}
		linked++
	}
	mode := "DRY-RUN"
	if apply {
		mode = "APPLIED"
	}
	log.Printf("%s created=%d linked=%d retired_one_time=%d conflicts=%d", mode, created, linked, retired, conflicts)
	if conflicts > 0 {
		os.Exit(2)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
