// Command tenant-apikey mints, rotates, and lists per-tenant API keys for the
// tenant-scoped send API (X-API-Key on /api/marketing/tenant/email).
//
// The plaintext key is printed exactly once; only its SHA-256 hash and a
// display prefix are stored on the tenant record.
//
// Usage:
//
//	go run ./marketing-service/cmd/tenant-apikey -list
//	go run ./marketing-service/cmd/tenant-apikey -tenant "Sentanyl"          # mint (fails if key exists)
//	go run ./marketing-service/cmd/tenant-apikey -tenant <hex-id> -rotate    # replace existing key
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

func main() {
	var (
		host   string
		port   string
		dbName string
		tenant string
		rotate bool
		list   bool
	)
	flag.StringVar(&host, "mongo-host", envOr("MONGO_HOST", "localhost"), "Mongo host")
	flag.StringVar(&port, "mongo-port", envOr("MONGO_PORT", "27017"), "Mongo port")
	flag.StringVar(&dbName, "mongo-db", envOr("MONGO_DB", "sentanyl_db"), "Mongo database name")
	flag.StringVar(&tenant, "tenant", "", "Tenant hex id or business_name")
	flag.BoolVar(&rotate, "rotate", false, "Replace an existing key")
	flag.BoolVar(&list, "list", false, "List tenants and their key prefixes")
	flag.Parse()

	db.MongoHost = host
	db.MongoPort = port
	db.MongoDB = dbName
	db.MongoDefaultCollectionName = "users"
	db.UsingLocalMongo = true
	db.InitMongoConnection()

	if list {
		listTenants()
		return
	}
	if tenant == "" {
		log.Fatal("either -list or -tenant is required")
	}

	t := findTenant(tenant)
	if t.APIKeyHash != "" && !rotate {
		log.Fatalf("tenant %q already has a key (prefix %s) — pass -rotate to replace it", t.BusinessName, t.APIKeyPrefix)
	}

	key := MintKey(t.Id)
	fmt.Printf("tenant:  %s (%s)\n", t.BusinessName, t.Id.Hex())
	fmt.Printf("api key: %s\n", key)
	fmt.Println("Store it now — only its hash is kept in the database.")
}

// MintKey generates a key and persists hash+prefix on the tenant.
func MintKey(tenantID bson.ObjectId) string {
	key, err := auth.GenerateAPIKey()
	if err != nil {
		log.Fatalf("generate key: %v", err)
	}
	err = db.GetCollection(pkgmodels.TenantCollection).UpdateId(tenantID, bson.M{
		"$set": bson.M{
			"api_key_hash":   auth.HashAPIKey(key),
			"api_key_prefix": auth.APIKeyPrefix(key),
		},
	})
	if err != nil {
		log.Fatalf("store key hash: %v", err)
	}
	return key
}

func findTenant(ref string) *pkgmodels.Tenant {
	var t pkgmodels.Tenant
	col := db.GetCollection(pkgmodels.TenantCollection)
	if bson.IsObjectIdHex(ref) {
		if err := col.FindId(bson.ObjectIdHex(ref)).One(&t); err == nil {
			return &t
		}
	}
	err := col.Find(bson.M{"business_name": ref, "timestamps.deleted_at": nil}).One(&t)
	if err != nil {
		log.Fatalf("tenant %q not found: %v", ref, err)
	}
	return &t
}

func listTenants() {
	var tenants []pkgmodels.Tenant
	err := db.GetCollection(pkgmodels.TenantCollection).
		Find(bson.M{"timestamps.deleted_at": nil}).All(&tenants)
	if err != nil {
		log.Fatalf("list tenants: %v", err)
	}
	for _, t := range tenants {
		prefix := t.APIKeyPrefix
		if prefix == "" {
			prefix = "(no key)"
		}
		fmt.Printf("%s  %-30s  %s\n", t.Id.Hex(), t.BusinessName, prefix)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
