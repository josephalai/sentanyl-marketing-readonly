package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterPurchasesRoutes wires the tenant-scoped purchase log endpoints used
// by the admin Purchases page and the per-contact revenue view. The routes
// live on the same legacy /api/tenant/* group used by the rest of the admin
// surface (see marketing-service/cmd/main.go).
func RegisterPurchasesRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.GET("/purchases", handleListPurchases)
	tenantAPI.GET("/purchases/:id", handleGetPurchase)
	tenantAPI.POST("/purchases/:id/refund", handleRefundPurchase)
}

// purchaseDTO is the row shape returned to the admin UI. It joins the
// PurchaseLog with the buyer contact and the purchased offer so the table
// renders without extra round-trips.
type purchaseDTO struct {
	ID             string    `json:"id"`
	PublicID       string    `json:"public_id"`
	CreatedAt      time.Time `json:"created_at"`
	Status         string    `json:"status"`
	Amount         float64   `json:"amount"`
	Currency       string    `json:"currency"`
	StripeChargeID string    `json:"stripe_charge_id,omitempty"`

	ContactID    string `json:"contact_id"`
	ContactEmail string `json:"contact_email,omitempty"`
	ContactName  string `json:"contact_name,omitempty"`

	OfferID    string `json:"offer_id,omitempty"`
	OfferTitle string `json:"offer_title,omitempty"`

	ProductID   string `json:"product_id,omitempty"`
	ProductName string `json:"product_name,omitempty"`
}

func handleListPurchases(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	q := bson.M{"tenant_id": tenantID}
	if status := c.Query("status"); status != "" {
		q["status"] = status
	}
	if contactID := c.Query("contact_id"); contactID != "" && bson.IsObjectIdHex(contactID) {
		q["user_id"] = bson.ObjectIdHex(contactID)
	}
	if offerID := c.Query("offer_id"); offerID != "" && bson.IsObjectIdHex(offerID) {
		q["offer_id"] = bson.ObjectIdHex(offerID)
	}
	if productID := c.Query("product_id"); productID != "" && bson.IsObjectIdHex(productID) {
		q["product_id"] = bson.ObjectIdHex(productID)
	}
	if from, ok := parseUnixSeconds(c.Query("from")); ok {
		q["timestamps.created_at"] = bson.M{"$gte": from}
	}
	if to, ok := parseUnixSeconds(c.Query("to")); ok {
		clause, _ := q["timestamps.created_at"].(bson.M)
		if clause == nil {
			clause = bson.M{}
		}
		clause["$lte"] = to
		q["timestamps.created_at"] = clause
	}

	limit := 100
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	var rows []pkgmodels.PurchaseLog
	if err := db.GetCollection(pkgmodels.PurchaseLogCollection).Find(q).
		Sort("-timestamps.created_at").Limit(limit).All(&rows); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list purchases"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    "ok",
		"purchases": hydratePurchaseRows(tenantID, rows),
	})
}

func handleGetPurchase(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	id := c.Param("id")
	q := bson.M{"tenant_id": tenantID}
	if bson.IsObjectIdHex(id) {
		q["_id"] = bson.ObjectIdHex(id)
	} else {
		q["public_id"] = id
	}
	var p pkgmodels.PurchaseLog
	if err := db.GetCollection(pkgmodels.PurchaseLogCollection).Find(q).One(&p); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "purchase not found"})
		return
	}
	hydrated := hydratePurchaseRows(tenantID, []pkgmodels.PurchaseLog{p})
	c.JSON(http.StatusOK, gin.H{"status": "ok", "purchase": hydrated[0]})
}

// hydratePurchaseRows joins each PurchaseLog row with its buyer User and the
// referenced Offer/Product. Done with batch lookups to keep the response
// snappy for the admin table — one query per related collection regardless
// of how many rows are returned.
func hydratePurchaseRows(tenantID bson.ObjectId, rows []pkgmodels.PurchaseLog) []purchaseDTO {
	out := make([]purchaseDTO, 0, len(rows))
	if len(rows) == 0 {
		return out
	}

	contactIDs := uniqueObjectIDs(func(i int) bson.ObjectId { return rows[i].UserId }, len(rows))
	offerIDs := uniqueObjectIDs(func(i int) bson.ObjectId { return rows[i].OfferID }, len(rows))
	productIDs := uniqueObjectIDs(func(i int) bson.ObjectId { return rows[i].ProductId }, len(rows))

	contacts := map[bson.ObjectId]pkgmodels.User{}
	if len(contactIDs) > 0 {
		var found []pkgmodels.User
		_ = db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
			"_id":       bson.M{"$in": contactIDs},
			"tenant_id": tenantID,
		}).All(&found)
		for _, u := range found {
			contacts[u.Id] = u
		}
	}
	offers := map[bson.ObjectId]pkgmodels.Offer{}
	if len(offerIDs) > 0 {
		var found []pkgmodels.Offer
		_ = db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
			"_id":       bson.M{"$in": offerIDs},
			"tenant_id": tenantID,
		}).All(&found)
		for _, o := range found {
			offers[o.Id] = o
		}
	}
	products := map[bson.ObjectId]pkgmodels.Product{}
	if len(productIDs) > 0 {
		var found []pkgmodels.Product
		_ = db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
			"_id":       bson.M{"$in": productIDs},
			"tenant_id": tenantID,
		}).All(&found)
		for _, p := range found {
			products[p.Id] = p
		}
	}

	for _, r := range rows {
		var created time.Time
		if r.SoftDeletes.CreatedAt != nil {
			created = *r.SoftDeletes.CreatedAt
		}
		dto := purchaseDTO{
			ID:             r.Id.Hex(),
			PublicID:       r.PublicId,
			CreatedAt:      created,
			Status:         r.Status,
			Amount:         r.Amount,
			Currency:       r.Currency,
			StripeChargeID: r.StripeChargeId,
			ContactID:      r.UserId.Hex(),
			OfferID:        r.OfferID.Hex(),
			ProductID:      r.ProductId.Hex(),
		}
		if u, ok := contacts[r.UserId]; ok {
			dto.ContactEmail = string(u.Email)
			dto.ContactName = nameOf(u)
		}
		if o, ok := offers[r.OfferID]; ok {
			dto.OfferTitle = o.Title
		}
		if p, ok := products[r.ProductId]; ok {
			dto.ProductName = p.Name
		}
		out = append(out, dto)
	}
	return out
}

func nameOf(u pkgmodels.User) string {
	first := u.Name.First
	last := u.Name.Last
	if first == "" && last == "" {
		return ""
	}
	if last == "" {
		return first
	}
	if first == "" {
		return last
	}
	return first + " " + last
}

// uniqueObjectIDs gathers the distinct (non-zero) ObjectIds returned by an
// accessor function. Used to build $in filters for the join queries above.
func uniqueObjectIDs(get func(i int) bson.ObjectId, n int) []bson.ObjectId {
	seen := map[bson.ObjectId]struct{}{}
	out := make([]bson.ObjectId, 0, n)
	for i := 0; i < n; i++ {
		id := get(i)
		if id == "" || !id.Valid() {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// parseUnixSeconds accepts either Unix seconds or RFC3339 dates. Returns false
// when the value is empty or unparseable so the caller can decide whether to
// 400 or to treat it as "filter not applied".
func parseUnixSeconds(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(n, 0), true
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// handleRefundPurchase issues a Stripe refund for the given purchase row and
// relies on the existing charge.refunded webhook (serve_stripe_webhook.go) to
// flip the PurchaseLog row(s), revoke entitlements, and strip granted badges.
// We intentionally don't pre-flip the row here — that would race with the
// webhook callback and risk double-flipping if the refund actually fails on
// Stripe's side. A pending column would be the cleanest UX upgrade.
func handleRefundPurchase(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	id := c.Param("id")
	q := bson.M{"tenant_id": tenantID}
	if bson.IsObjectIdHex(id) {
		q["_id"] = bson.ObjectIdHex(id)
	} else {
		q["public_id"] = id
	}
	var p pkgmodels.PurchaseLog
	if err := db.GetCollection(pkgmodels.PurchaseLogCollection).Find(q).One(&p); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "purchase not found"})
		return
	}
	if p.Status == "refunded" {
		c.JSON(http.StatusConflict, gin.H{"error": "purchase already refunded"})
		return
	}
	if p.StripeChargeId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "purchase has no stripe charge — cannot refund through Stripe"})
		return
	}

	var tenant pkgmodels.Tenant
	if err := db.GetCollection(pkgmodels.TenantCollection).FindId(tenantID).One(&tenant); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "tenant lookup failed"})
		return
	}
	stripeKey := tenant.StripeSecretKey
	stripeAcct := ""
	if stripeKey == "" && tenant.StripeConnectAccountID != "" {
		stripeKey = os.Getenv("STRIPE_PLATFORM_SECRET_KEY")
		stripeAcct = tenant.StripeConnectAccountID
	}
	if stripeKey == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "stripe not configured for this tenant"})
		return
	}

	refundID, err := createStripeRefund(stripeKey, stripeAcct, p.StripeChargeId, tenantID, p.OfferID, string(loadContactEmail(tenantID, p.UserId)))
	if err != nil {
		log.Printf("refund failed: charge=%s err=%v", p.StripeChargeId, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "stripe refund failed: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":    "ok",
		"refund_id": refundID,
		"note":      "Stripe will fire charge.refunded; the row will flip to refunded once the webhook lands.",
	})
}

// createStripeRefund posts to Stripe's /v1/refunds API. Metadata mirrors what
// the checkout session originally carried so processChargeRefunded can resolve
// (offer, contact_email) without doing extra Stripe lookups.
func createStripeRefund(stripeKey, stripeAccount, chargeOrPI string, tenantID bson.ObjectId, offerID bson.ObjectId, contactEmail string) (string, error) {
	form := url.Values{}
	// Stripe's /v1/refunds accepts either `charge` or `payment_intent`. The
	// PurchaseLog stores either depending on which one the session payload
	// surfaced — try payment_intent first since checkout sessions use it.
	if strings.HasPrefix(chargeOrPI, "pi_") {
		form.Set("payment_intent", chargeOrPI)
	} else {
		form.Set("charge", chargeOrPI)
	}
	form.Set("metadata[tenant_id]", tenantID.Hex())
	if offerID.Valid() {
		form.Set("metadata[offer_id]", offerID.Hex())
	}
	if contactEmail != "" {
		form.Set("metadata[contact_email]", contactEmail)
	}

	req, err := http.NewRequest("POST", "https://api.stripe.com/v1/refunds", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(stripeKey, "")
	if stripeAccount != "" {
		req.Header.Set("Stripe-Account", stripeAccount)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out struct {
		ID    string `json:"id"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode refund response: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("stripe: %s", out.Error.Message)
	}
	if out.ID == "" {
		return "", fmt.Errorf("stripe returned no refund id")
	}
	return out.ID, nil
}

// loadContactEmail returns the contact's email by id, blank on error. Used as
// a best-effort metadata field on the Stripe Refund so the inbound webhook
// can resolve the buyer without a separate User lookup.
func loadContactEmail(tenantID, contactID bson.ObjectId) pkgmodels.EmailAddress {
	var u pkgmodels.User
	if err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
		"_id":       contactID,
		"tenant_id": tenantID,
	}).One(&u); err != nil {
		return ""
	}
	return u.Email
}
