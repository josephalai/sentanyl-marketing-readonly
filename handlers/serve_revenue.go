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

// handleRevenueSummary returns aggregate paid totals across the optional
// from/to date window plus a per-day series suitable for a small chart.
// Refunded purchases are excluded from totals but counted separately.
func handleRevenueSummary(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	rows := loadPurchasesForRevenue(tenantID, c)

	var paidCount, refundedCount int
	var paidTotal, refundedTotal float64
	currency := ""
	uniqueContacts := map[bson.ObjectId]struct{}{}
	dailyTotals := map[string]float64{}

	for _, r := range rows {
		if currency == "" && r.Currency != "" {
			currency = r.Currency
		}
		switch r.Status {
		case "refunded":
			refundedCount++
			refundedTotal += r.Amount
			continue
		}
		paidCount++
		paidTotal += r.Amount
		uniqueContacts[r.UserId] = struct{}{}
		if r.SoftDeletes.CreatedAt != nil {
			d := r.SoftDeletes.CreatedAt.UTC().Format("2006-01-02")
			dailyTotals[d] += r.Amount
		}
	}

	// Render the daily series as an ordered slice so the frontend can plot
	// it without re-sorting.
	type point struct {
		Date   string  `json:"date"`
		Amount float64 `json:"amount"`
	}
	keys := make([]string, 0, len(dailyTotals))
	for k := range dailyTotals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	series := make([]point, 0, len(keys))
	for _, k := range keys {
		series = append(series, point{Date: k, Amount: dailyTotals[k]})
	}

	avg := 0.0
	if paidCount > 0 {
		avg = paidTotal / float64(paidCount)
	}

	c.JSON(http.StatusOK, gin.H{
		"status":            "ok",
		"currency":          currency,
		"paid_count":        paidCount,
		"paid_total":        paidTotal,
		"refunded_count":    refundedCount,
		"refunded_total":    refundedTotal,
		"net_total":         paidTotal - refundedTotal,
		"unique_buyers":     len(uniqueContacts),
		"average_purchase":  avg,
		"daily_series":      series,
	})
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
