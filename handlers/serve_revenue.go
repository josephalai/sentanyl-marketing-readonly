package handlers

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterRevenueRoutes wires the tenant-scoped revenue rollup endpoints used
// by the admin Revenue page. Mounted on the same /api/tenant/* group as the
// rest of the admin surface.
func RegisterRevenueRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.GET("/revenue/summary", handleRevenueSummary)
	tenantAPI.GET("/revenue/by-product", handleRevenueByProduct)
	tenantAPI.GET("/revenue/by-contact", handleRevenueByContact)
	tenantAPI.GET("/revenue/contact/:contactId", handleRevenueForContact)
}

// revenueGroup is one currency's aggregation output from the DB pipeline.
type revenueGroup struct {
	Currency      string `bson:"_id"`
	GrossMinor    int64  `bson:"gross_minor"`
	RefundedMinor int64  `bson:"refunded_minor"`
	PaidCount     int    `bson:"paid_count"`
	RefundedCount int    `bson:"refunded_count"`
}

// rollupRevenue converts per-currency aggregation groups into the response
// rollup, computing net = gross − refunded per currency (never across
// currencies) and ordering by net descending. Pure — testable without a DB.
func rollupRevenue(groups []revenueGroup) []currencyRevenue {
	out := make([]currencyRevenue, 0, len(groups))
	for _, g := range groups {
		out = append(out, currencyRevenue{
			Currency:      g.Currency,
			GrossMinor:    g.GrossMinor,
			RefundedMinor: g.RefundedMinor,
			NetMinor:      g.GrossMinor - g.RefundedMinor,
			PaidCount:     g.PaidCount,
			RefundedCount: g.RefundedCount,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NetMinor > out[j].NetMinor })
	return out
}

// currencyRevenue is the per-currency rollup in integer minor units (ANA-003).
type currencyRevenue struct {
	Currency      string `json:"currency"`
	GrossMinor    int64  `json:"gross_minor"`    // every sale that happened
	RefundedMinor int64  `json:"refunded_minor"` // refunded portion
	NetMinor      int64  `json:"net_minor"`      // gross - refunded
	PaidCount     int    `json:"paid_count"`
	RefundedCount int    `json:"refunded_count"`
	UniqueBuyers  int    `json:"unique_buyers"`
}

// handleRevenueSummary returns revenue rolled up per currency in integer minor
// units, computed by a database aggregation over the full window (no silent row
// cap). Correct net accounting (ANA-001): a refund of a $100 sale nets to $0,
// because the sale is counted in gross and the refund is subtracted — the old
// code excluded refunded sales from gross while still subtracting them, so a
// refund read as −$100.
func handleRevenueSummary(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	match := revenueMatch(tenantID, c)

	// ANA-002/003: aggregate server-side, grouped by currency, in integer
	// minor units. amount is stored in major units (float), so multiply by 100
	// and round inside the pipeline; never sum across currencies.
	minorExpr := bson.M{"$round": bson.M{"$multiply": []interface{}{"$amount", 100}}}
	pipeline := []bson.M{
		{"$match": match},
		{"$group": bson.M{
			"_id":        "$currency",
			"gross_minor": bson.M{"$sum": minorExpr},
			"refunded_minor": bson.M{"$sum": bson.M{"$cond": []interface{}{
				bson.M{"$eq": []interface{}{"$status", "refunded"}}, minorExpr, 0,
			}}},
			"paid_count": bson.M{"$sum": bson.M{"$cond": []interface{}{
				bson.M{"$eq": []interface{}{"$status", "refunded"}}, 0, 1,
			}}},
			"refunded_count": bson.M{"$sum": bson.M{"$cond": []interface{}{
				bson.M{"$eq": []interface{}{"$status", "refunded"}}, 1, 0,
			}}},
		}},
	}
	var groups []revenueGroup
	if err := db.GetCollection(pkgmodels.PurchaseLogCollection).Pipe(pipeline).All(&groups); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "revenue aggregation failed"})
		return
	}

	byCurrency := rollupRevenue(groups)
	for i := range byCurrency {
		byCurrency[i].UniqueBuyers = revenueUniqueBuyers(tenantID, match, byCurrency[i].Currency)
	}

	// Backward-compatible top-level fields for the admin Revenue page, computed
	// for the primary (highest-net) currency in major units. `paid_total` is
	// gross sales; `net_total` = gross − refunded (now correct).
	resp := gin.H{
		"status":      "ok",
		"by_currency": byCurrency,
		"complete":    true, // full aggregation, no silent row cap
		"source":      "purchase_logs",
		"as_of":       time.Now().UTC().Format(time.RFC3339),
	}
	if len(byCurrency) > 0 {
		p := byCurrency[0]
		avg := 0.0
		if p.PaidCount > 0 {
			avg = float64(p.NetMinor) / 100.0 / float64(p.PaidCount)
		}
		resp["currency"] = p.Currency
		resp["paid_total"] = float64(p.GrossMinor) / 100.0
		resp["refunded_total"] = float64(p.RefundedMinor) / 100.0
		resp["net_total"] = float64(p.NetMinor) / 100.0
		resp["paid_count"] = p.PaidCount
		resp["refunded_count"] = p.RefundedCount
		resp["unique_buyers"] = p.UniqueBuyers
		resp["average_purchase"] = avg
		resp["daily_series"] = revenueDailySeries(tenantID, match, p.Currency)
	} else {
		resp["currency"] = ""
		resp["paid_total"] = 0.0
		resp["refunded_total"] = 0.0
		resp["net_total"] = 0.0
		resp["daily_series"] = []interface{}{}
	}
	c.JSON(http.StatusOK, resp)
}

// revenueDailySeries returns the per-day net (gross − refunded) for one
// currency, in major units, ordered by date — computed by DB aggregation so it
// is not subject to any row cap.
func revenueDailySeries(tenantID bson.ObjectId, match bson.M, currency string) []map[string]interface{} {
	m := bson.M{}
	for k, v := range match {
		m[k] = v
	}
	m["currency"] = currency
	minorExpr := bson.M{"$round": bson.M{"$multiply": []interface{}{"$amount", 100}}}
	pipeline := []bson.M{
		{"$match": m},
		{"$group": bson.M{
			"_id": bson.M{"$dateToString": bson.M{"format": "%Y-%m-%d", "date": "$timestamps.created_at"}},
			"net_minor": bson.M{"$sum": bson.M{"$cond": []interface{}{
				bson.M{"$eq": []interface{}{"$status", "refunded"}}, bson.M{"$multiply": []interface{}{minorExpr, -1}}, minorExpr,
			}}},
		}},
		{"$sort": bson.M{"_id": 1}},
	}
	var raw []struct {
		Date     string `bson:"_id"`
		NetMinor int64  `bson:"net_minor"`
	}
	_ = db.GetCollection(pkgmodels.PurchaseLogCollection).Pipe(pipeline).All(&raw)
	out := make([]map[string]interface{}, 0, len(raw))
	for _, r := range raw {
		if r.Date == "" {
			continue
		}
		out = append(out, map[string]interface{}{"date": r.Date, "amount": float64(r.NetMinor) / 100.0})
	}
	return out
}

// revenueUniqueBuyers counts distinct non-refunded buyers for one currency.
func revenueUniqueBuyers(tenantID bson.ObjectId, match bson.M, currency string) int {
	q := bson.M{}
	for k, v := range match {
		q[k] = v
	}
	q["currency"] = currency
	q["status"] = bson.M{"$ne": "refunded"}
	var ids []bson.ObjectId
	_ = db.GetCollection(pkgmodels.PurchaseLogCollection).Find(q).Distinct("user_id", &ids)
	return len(ids)
}

// revenueMatch builds the tenant + optional date-window match shared by the
// revenue aggregation and the legacy row loaders.
func revenueMatch(tenantID bson.ObjectId, c *gin.Context) bson.M {
	q := bson.M{"tenant_id": tenantID}
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
	return q
}

// handleRevenueByProduct returns paid totals + counts grouped by product, hot
// list first. Refunded rows are excluded.
func handleRevenueByProduct(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	rows := loadPurchasesForRevenue(tenantID, c)

	type row struct {
		ProductID   string  `json:"product_id"`
		ProductName string  `json:"product_name"`
		OfferID     string  `json:"offer_id,omitempty"`
		OfferTitle  string  `json:"offer_title,omitempty"`
		Count       int     `json:"count"`
		Total       float64 `json:"total"`
	}

	totals := map[bson.ObjectId]*row{}
	for _, r := range rows {
		if r.Status == "refunded" {
			continue
		}
		key := r.ProductId
		if !key.Valid() {
			key = r.OfferID
		}
		if !key.Valid() {
			continue
		}
		entry := totals[key]
		if entry == nil {
			entry = &row{
				ProductID: hexOrEmpty(r.ProductId),
				OfferID:   hexOrEmpty(r.OfferID),
			}
			totals[key] = entry
		}
		entry.Count++
		entry.Total += r.Amount
	}

	productNames := map[bson.ObjectId]string{}
	if len(totals) > 0 {
		var ids []bson.ObjectId
		for k := range totals {
			ids = append(ids, k)
		}
		var products []pkgmodels.Product
		_ = db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
			"_id":       bson.M{"$in": ids},
			"tenant_id": tenantID,
		}).All(&products)
		for _, p := range products {
			productNames[p.Id] = p.Name
		}
		var offers []pkgmodels.Offer
		_ = db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
			"_id":       bson.M{"$in": ids},
			"tenant_id": tenantID,
		}).All(&offers)
		for _, o := range offers {
			productNames[o.Id] = o.Title
		}
	}

	out := make([]row, 0, len(totals))
	for k, v := range totals {
		v.ProductName = productNames[k]
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })

	c.JSON(http.StatusOK, gin.H{"status": "ok", "rows": out})
}

// handleRevenueByContact returns the buyers ranked by lifetime spend on this
// tenant. Refunded rows are excluded from the ranking but the count of
// refunds per contact is surfaced for follow-up.
func handleRevenueByContact(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	rows := loadPurchasesForRevenue(tenantID, c)

	type row struct {
		ContactID    string    `json:"contact_id"`
		ContactEmail string    `json:"contact_email,omitempty"`
		ContactName  string    `json:"contact_name,omitempty"`
		Count        int       `json:"count"`
		Refunds      int       `json:"refunds"`
		Total        float64   `json:"total"`
		LastAt       time.Time `json:"last_at,omitempty"`
	}

	totals := map[bson.ObjectId]*row{}
	for _, r := range rows {
		if !r.UserId.Valid() {
			continue
		}
		entry := totals[r.UserId]
		if entry == nil {
			entry = &row{ContactID: r.UserId.Hex()}
			totals[r.UserId] = entry
		}
		if r.Status == "refunded" {
			entry.Refunds++
			continue
		}
		entry.Count++
		entry.Total += r.Amount
		if r.SoftDeletes.CreatedAt != nil && r.SoftDeletes.CreatedAt.After(entry.LastAt) {
			entry.LastAt = *r.SoftDeletes.CreatedAt
		}
	}

	if len(totals) > 0 {
		var ids []bson.ObjectId
		for k := range totals {
			ids = append(ids, k)
		}
		var contacts []pkgmodels.User
		_ = db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
			"_id":       bson.M{"$in": ids},
			"tenant_id": tenantID,
		}).All(&contacts)
		for _, u := range contacts {
			if entry := totals[u.Id]; entry != nil {
				entry.ContactEmail = string(u.Email)
				entry.ContactName = nameOf(u)
			}
		}
	}

	out := make([]row, 0, len(totals))
	for _, v := range totals {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })

	c.JSON(http.StatusOK, gin.H{"status": "ok", "rows": out})
}

// handleRevenueForContact returns the per-purchase history for a single
// contact, plus their lifetime total. Used by the customer-revenue page.
func handleRevenueForContact(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	contactRef := c.Param("contactId")
	// Accept either an ObjectId hex (from purchase rows where User.Id is
	// already on hand) or a User.PublicId (from the contacts page where
	// User.Id is JSON-hidden). Both routes converge on the same User row.
	q := bson.M{"tenant_id": tenantID}
	if bson.IsObjectIdHex(contactRef) {
		q["_id"] = bson.ObjectIdHex(contactRef)
	} else if contactRef != "" {
		q["public_id"] = contactRef
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "contact id required"})
		return
	}
	var contact pkgmodels.User
	if err := db.GetCollection(pkgmodels.UserCollection).Find(q).One(&contact); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "contact not found"})
		return
	}
	cid := contact.Id

	var rows []pkgmodels.PurchaseLog
	_ = db.GetCollection(pkgmodels.PurchaseLogCollection).Find(bson.M{
		"tenant_id": tenantID,
		"user_id":   cid,
	}).Sort("-timestamps.created_at").All(&rows)

	hydrated := hydratePurchaseRows(tenantID, rows)

	var total float64
	currency := ""
	for _, r := range hydrated {
		if currency == "" && r.Currency != "" {
			currency = r.Currency
		}
		if r.Status != "refunded" {
			total += r.Amount
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"contact": gin.H{
			"id":    contact.Id.Hex(),
			"email": string(contact.Email),
			"name":  nameOf(contact),
		},
		"lifetime_total": total,
		"currency":       currency,
		"purchases":      hydrated,
	})
}

// loadPurchasesForRevenue returns every PurchaseLog row for the tenant within
// the optional from/to window. Shared across the rollup handlers so all
// surfaces apply the same filter semantics.
func loadPurchasesForRevenue(tenantID bson.ObjectId, c *gin.Context) []pkgmodels.PurchaseLog {
	q := bson.M{"tenant_id": tenantID}
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
	limit := 5000
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50000 {
			limit = n
		}
	}
	var rows []pkgmodels.PurchaseLog
	_ = db.GetCollection(pkgmodels.PurchaseLogCollection).Find(q).Limit(limit).All(&rows)
	return rows
}

func hexOrEmpty(id bson.ObjectId) string {
	if !id.Valid() {
		return ""
	}
	return id.Hex()
}
