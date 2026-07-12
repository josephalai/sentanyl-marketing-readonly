// Command encrypt-tenant-secrets migrates plaintext tenant secrets
// (stripe_secret_key, stripe_webhook_secret, mailgun_api_key, brevo_api_key) to
// AES-GCM at-rest encryption. Values already tagged with the "enc:v1:" prefix
// are left untouched, so the command is idempotent and safe to re-run.
//
// It refuses to run unless SENTANYL_ENCRYPTION_KEY (>=32 bytes) is set —
// otherwise utils.EncryptSecret is a no-op and the migration would silently do
// nothing.
//
// Usage:
//
//	SENTANYL_ENCRYPTION_KEY=... go run ./marketing-service/cmd/encrypt-tenant-secrets            # dry-run
//	SENTANYL_ENCRYPTION_KEY=... go run ./marketing-service/cmd/encrypt-tenant-secrets -apply     # write
package main

import (
	"flag"
	"log"
	"os"
	"strings"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

const encPrefix = "enc:v1:"

func main() {
	var (
		host   string
		port   string
		dbName string
		apply  bool
	)
	flag.StringVar(&host, "mongo-host", envOr("MONGO_HOST", "localhost"), "Mongo host")
	flag.StringVar(&port, "mongo-port", envOr("MONGO_PORT", "27017"), "Mongo port")
	flag.StringVar(&dbName, "mongo-db", envOr("MONGO_DB", "sentanyl_db"), "Mongo database name")
	flag.BoolVar(&apply, "apply", false, "Write changes (default is dry-run)")
	flag.Parse()

	if len(os.Getenv("SENTANYL_ENCRYPTION_KEY")) < 32 {
		log.Fatal("SENTANYL_ENCRYPTION_KEY (>=32 bytes) must be set; refusing to run so we don't no-op")
	}

	db.MongoHost = host
	db.MongoPort = port
	db.MongoDB = dbName
	db.MongoDefaultCollectionName = "users"
	db.UsingLocalMongo = true
	db.InitMongoConnection()

	col := db.GetCollection(pkgmodels.TenantCollection)
	var tenants []pkgmodels.Tenant
	if err := col.Find(bson.M{}).All(&tenants); err != nil {
		log.Fatalf("list tenants: %v", err)
	}

	fields := []struct {
		name string
		get  func(*pkgmodels.Tenant) string
	}{
		{"stripe_secret_key", func(t *pkgmodels.Tenant) string { return t.StripeSecretKey }},
		{"stripe_webhook_secret", func(t *pkgmodels.Tenant) string { return t.StripeWebhookSecret }},
		{"mailgun_api_key", func(t *pkgmodels.Tenant) string { return t.MailgunAPIKey }},
		{"brevo_api_key", func(t *pkgmodels.Tenant) string { return t.BrevoAPIKey }},
	}

	changed, skipped := 0, 0
	for i := range tenants {
		t := &tenants[i]
		set := bson.M{}
		for _, f := range fields {
			v := f.get(t)
			if v == "" || strings.HasPrefix(v, encPrefix) {
				continue
			}
			enc, err := utils.EncryptSecret(v)
			if err != nil {
				log.Fatalf("encrypt %s for tenant %s: %v", f.name, t.Id.Hex(), err)
			}
			set[f.name] = enc
		}
		if len(set) == 0 {
			skipped++
			continue
		}
		changed++
		keys := make([]string, 0, len(set))
		for k := range set {
			keys = append(keys, k)
		}
		log.Printf("tenant %s (%s): encrypt %s", t.Id.Hex(), t.BusinessName, strings.Join(keys, ", "))
		if apply {
			if err := col.Update(bson.M{"_id": t.Id}, bson.M{"$set": set}); err != nil {
				log.Fatalf("update tenant %s: %v", t.Id.Hex(), err)
			}
		}
	}

	mode := "DRY-RUN (no writes; pass -apply to commit)"
	if apply {
		mode = "APPLIED"
	}
	log.Printf("%s — %d tenant(s) encrypted, %d already-clean/empty", mode, changed, skipped)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
