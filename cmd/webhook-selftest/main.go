// Command webhook-selftest proves the at-rest-encrypted Stripe webhook secret
// (P0-2) can be decrypted and used to sign a request the live webhook accepts.
//
// It loads a tenant, decrypts stripe_webhook_secret, signs a BENIGN event type
// the webhook handler ignores (so there is no provisioning side-effect), posts
// it to the marketing webhook, and prints the HTTP status. A 200 proves the
// running service decrypts the same value this tool does (matching key). The
// secret itself is never printed.
//
// Usage (inside the compose network):
//
//	SENTANYL_ENCRYPTION_KEY=... MONGO_HOST=mongo webhook-selftest -tenant <hex> -url http://marketing-service:8082
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

func main() {
	var tenantHex, marketingURL, host string
	flag.StringVar(&tenantHex, "tenant", "", "tenant hex id")
	flag.StringVar(&marketingURL, "url", "http://marketing-service:8082", "marketing base URL")
	flag.StringVar(&host, "mongo-host", envOr("MONGO_HOST", "mongo"), "Mongo host")
	flag.Parse()
	if !bson.IsObjectIdHex(tenantHex) {
		log.Fatal("-tenant must be a hex object id")
	}

	db.MongoHost = host
	db.MongoPort = envOr("MONGO_PORT", "27017")
	db.MongoDB = envOr("MONGO_DB", "sentanyl_db")
	db.MongoDefaultCollectionName = "users"
	db.UsingLocalMongo = true
	db.InitMongoConnection()

	var tenant pkgmodels.Tenant
	if err := db.GetCollection(pkgmodels.TenantCollection).FindId(bson.ObjectIdHex(tenantHex)).One(&tenant); err != nil {
		log.Fatalf("load tenant: %v", err)
	}
	stored := tenant.StripeWebhookSecret
	secret := utils.DecryptSecret(stored)
	if secret == "" {
		log.Fatalf("decrypt failed or empty (stored prefix present=%v)", len(stored) > 7)
	}
	log.Printf("stored is %sencrypted; decrypt produced a %d-char secret",
		map[bool]string{true: "", false: "NOT "}[len(stored) > 7 && stored[:7] == "enc:v1:"], len(secret))

	// Benign event the handler acknowledges but does not act on.
	body := []byte(`{"id":"evt_selftest","type":"selftest.ping","data":{"object":{}}}`)
	ts := fmt.Sprintf("%d", time.Now().Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(body)))
	sig := hex.EncodeToString(mac.Sum(nil))

	req, _ := http.NewRequest("POST", marketingURL+"/api/marketing/stripe/webhook?tenant_id="+tenantHex, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", "t="+ts+",v1="+sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	fmt.Printf("webhook responded %d: %s\n", resp.StatusCode, string(rb))
	if resp.StatusCode == 200 {
		fmt.Println("PASS — running service verified a signature made with the decrypted secret")
	} else {
		fmt.Println("FAIL — signature rejected; the service could not decrypt to the same secret")
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
