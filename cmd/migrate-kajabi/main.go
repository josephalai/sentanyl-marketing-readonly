// Command migrate-kajabi drives a Kajabi export-file migration from the CLI
// (MIG-001..005): create a project from a directory of export files, then
// validate, dry-run, import, or roll back.
//
// Expected files in -dir (all optional, at least one importable):
//
//	contacts.csv transactions.csv offers.csv products.csv grants.csv
//	courses.json assets.csv
//
// Usage:
//
//	go run ./marketing-service/cmd/migrate-kajabi -tenant <hex> -dir ./export -validate
//	go run ./marketing-service/cmd/migrate-kajabi -tenant <hex> -dir ./export -dry-run
//	go run ./marketing-service/cmd/migrate-kajabi -tenant <hex> -dir ./export -apply
//	go run ./marketing-service/cmd/migrate-kajabi -tenant <hex> -project <publicId> -rollback
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/migration"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

func main() {
	var (
		host, port, dbName    string
		tenant, dir, project  string
		validate, dryRun      bool
		apply, rollback       bool
	)
	flag.StringVar(&host, "mongo-host", envOr("MONGO_HOST", "localhost"), "Mongo host")
	flag.StringVar(&port, "mongo-port", envOr("MONGO_PORT", "27017"), "Mongo port")
	flag.StringVar(&dbName, "mongo-db", envOr("MONGO_DB", "sentanyl_db"), "Mongo database name")
	flag.StringVar(&tenant, "tenant", "", "Tenant hex id (required)")
	flag.StringVar(&dir, "dir", "", "Directory holding the export files")
	flag.StringVar(&project, "project", "", "Existing project public id (reuse instead of creating)")
	flag.BoolVar(&validate, "validate", false, "Parse + validation report only")
	flag.BoolVar(&dryRun, "dry-run", false, "Simulate: report creates/matches without writing")
	flag.BoolVar(&apply, "apply", false, "Execute the import")
	flag.BoolVar(&rollback, "rollback", false, "Delete rows created by -project")
	flag.Parse()

	if !bson.IsObjectIdHex(tenant) {
		log.Fatal("-tenant must be a 24-hex tenant id")
	}
	tenantID := bson.ObjectIdHex(tenant)

	db.MongoHost = host
	db.MongoPort = port
	db.MongoDB = dbName
	db.MongoDefaultCollectionName = "users"
	db.UsingLocalMongo = true
	db.InitMongoConnection()
	migration.EnsureIndexes()

	var p *pkgmodels.MigrationProject
	var err error
	if project != "" {
		p, err = migration.LoadProject(tenantID, project)
		if err != nil {
			log.Fatalf("project %s not found for tenant: %v", project, err)
		}
	} else {
		p = pkgmodels.NewMigrationProject(tenantID, migration.SourceKajabi)
		if err := db.GetCollection(pkgmodels.MigrationProjectCollection).Insert(p); err != nil {
			log.Fatalf("create project: %v", err)
		}
		fmt.Printf("project created: %s\n", p.PublicId)
	}

	if dir != "" {
		for kind, file := range map[string]string{
			"contacts": "contacts.csv", "products": "products.csv", "offers": "offers.csv",
			"transactions": "transactions.csv", "grants": "grants.csv",
			"courses": "courses.json", "assets": "assets.csv",
		} {
			path := filepath.Join(dir, file)
			content, err := os.ReadFile(path)
			if err != nil {
				continue // optional file absent
			}
			if err := migration.StoreFile(p, kind, file, content); err != nil {
				log.Fatalf("store %s: %v", file, err)
			}
			fmt.Printf("stored %s (%d bytes)\n", file, len(content))
		}
	}

	var report bson.M
	switch {
	case rollback:
		report, err = migration.Rollback(p)
	case apply:
		report, err = migration.Execute(p)
	case dryRun:
		report, err = migration.DryRun(p)
	default:
		report, err = migration.Validate(p)
	}
	if err != nil {
		log.Fatalf("run failed: %v", err)
	}
	out, _ := json.MarshalIndent(bson.M{"project": p.PublicId, "status": p.Status, "report": report}, "", "  ")
	fmt.Println(string(out))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
