// Command seed-email-gateway idempotently provisions the email-gateway tenants,
// adopts the already-registered PMTA sending domains into per-tenant records
// (Mongo backfill — no DNS/DKIM churn; keys live only on the VPS), and mints
// each tenant's API key if it doesn't have one.
//
// Domain spec format: <domain>:<tenant business name>[:<selector>]
// (selector defaults to s1; VMTA is always vm-<domain>).
//
// Usage:
//
//	go run ./marketing-service/cmd/seed-email-gateway            # default 4 domains / 3 tenants
//	go run ./marketing-service/cmd/seed-email-gateway \
//	   -owner-email you@example.com -owner-password secret       # also create AccountUsers
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

var defaultDomainSpecs = []string{
	"sentanyl.com:Sentanyl",
	"sendhero.co:Sentanyl",
	"mynevillegoddard.com:My Neville Goddard",
	"sovrinmind.com:SovrinMind",
}

type domainSpec struct {
	Domain   string
	Tenant   string
	Selector string
}

func main() {
	var (
		host          string
		port          string
		dbName        string
		specsCSV      string
		ownerEmail    string
		ownerPassword string
	)
	flag.StringVar(&host, "mongo-host", envOr("MONGO_HOST", "localhost"), "Mongo host")
	flag.StringVar(&port, "mongo-port", envOr("MONGO_PORT", "27017"), "Mongo port")
	flag.StringVar(&dbName, "mongo-db", envOr("MONGO_DB", "sentanyl_db"), "Mongo database name")
	flag.StringVar(&specsCSV, "domains", strings.Join(defaultDomainSpecs, ","), "Comma-separated domain:tenant[:selector] specs")
	flag.StringVar(&ownerEmail, "owner-email", "", "Create an owner AccountUser with this email for each tenant (optional)")
	flag.StringVar(&ownerPassword, "owner-password", "", "Password for the owner AccountUser")
	flag.Parse()

	db.MongoHost = host
	db.MongoPort = port
	db.MongoDB = dbName
	db.MongoDefaultCollectionName = "users"
	db.UsingLocalMongo = true
	db.InitMongoConnection()

	specs := parseSpecs(specsCSV)

	tenants := map[string]*pkgmodels.Tenant{}
	for _, s := range specs {
		if _, ok := tenants[s.Tenant]; !ok {
			tenants[s.Tenant] = upsertTenant(s.Tenant)
		}
	}

	for _, s := range specs {
		upsertSendingDomain(s, tenants[s.Tenant])
	}

	if ownerEmail != "" && ownerPassword != "" {
		for _, t := range tenants {
			upsertOwner(t, ownerEmail, ownerPassword)
		}
	}

	fmt.Println("\n=== API keys (shown once — only hashes are stored) ===")
	for name, t := range tenants {
		if t.APIKeyHash != "" {
			fmt.Printf("%-22s %s  (existing key, prefix %s — use tenant-apikey -rotate to replace)\n", name, t.Id.Hex(), t.APIKeyPrefix)
			continue
		}
		key := mintKey(t.Id)
		fmt.Printf("%-22s %s  %s\n", name, t.Id.Hex(), key)
	}
}

func parseSpecs(csv string) []domainSpec {
	var out []domainSpec
	for _, raw := range strings.Split(csv, ",") {
		parts := strings.Split(strings.TrimSpace(raw), ":")
		if len(parts) < 2 {
			log.Fatalf("bad domain spec %q (want domain:tenant[:selector])", raw)
		}
		s := domainSpec{Domain: strings.ToLower(parts[0]), Tenant: parts[1], Selector: "s1"}
		if len(parts) > 2 && parts[2] != "" {
			s.Selector = parts[2]
		}
		out = append(out, s)
	}
	return out
}

func upsertTenant(businessName string) *pkgmodels.Tenant {
	col := db.GetCollection(pkgmodels.TenantCollection)
	var t pkgmodels.Tenant
	err := col.Find(bson.M{"business_name": businessName, "timestamps.deleted_at": nil}).One(&t)
	if err == nil {
		fmt.Printf("tenant exists: %-22s %s\n", businessName, t.Id.Hex())
		return &t
	}
	nt := pkgmodels.NewTenant(businessName)
	nt.SubscriptionStatus = "active"
	if err := col.Insert(nt); err != nil {
		log.Fatalf("insert tenant %q: %v", businessName, err)
	}
	fmt.Printf("tenant created: %-21s %s\n", businessName, nt.Id.Hex())
	return nt
}

func upsertSendingDomain(s domainSpec, t *pkgmodels.Tenant) {
	col := db.GetCollection(pkgmodels.SendingDomainCollection)
	n, err := col.Find(bson.M{
		"domain":                s.Domain,
		"creator_id":            t.Id.Hex(),
		"timestamps.deleted_at": nil,
	}).Count()
	if err != nil {
		log.Fatalf("query sending_domains for %s: %v", s.Domain, err)
	}
	if n > 0 {
		fmt.Printf("domain exists: %-22s → %s\n", s.Domain, t.BusinessName)
		return
	}
	sd := pkgmodels.NewSendingDomain()
	sd.CreatorId = t.Id.Hex()
	sd.Domain = s.Domain
	sd.Selector = s.Selector
	sd.VMTA = "vm-" + s.Domain
	sd.Status = pkgmodels.DomainStatusActive
	if err := col.Insert(sd); err != nil {
		log.Fatalf("insert sending_domain %s: %v", s.Domain, err)
	}
	fmt.Printf("domain adopted: %-21s → %s (vmta %s, selector %s)\n", s.Domain, t.BusinessName, sd.VMTA, sd.Selector)
}

func upsertOwner(t *pkgmodels.Tenant, email, password string) {
	col := db.GetCollection(pkgmodels.AccountUserCollection)
	n, err := col.Find(bson.M{"email": email, "tenant_id": t.Id, "timestamps.deleted_at": nil}).Count()
	if err != nil {
		log.Fatalf("query account_users: %v", err)
	}
	if n > 0 {
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Fatalf("hash password: %v", err)
	}
	u := pkgmodels.NewAccountUser(email, t.Id)
	u.PasswordHash = hash
	if err := col.Insert(u); err != nil {
		log.Fatalf("insert account_user for %s: %v", t.BusinessName, err)
	}
	fmt.Printf("owner created: %s → %s\n", email, t.BusinessName)
}

func mintKey(tenantID bson.ObjectId) string {
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
