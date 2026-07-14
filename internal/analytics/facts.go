package analytics

import (
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/models"
)

// DefaultAttributionWindowDays is the last-touch attribution window; a
// purchase attributes to the newest touch no older than this. Overridable
// with ATTRIBUTION_WINDOW_DAYS. The window used is stamped on each fact.
const DefaultAttributionWindowDays = 30

// EnsureIndexes creates the revenue_facts indexes: one fact per
// (source log, kind) — the idempotency contract that makes writes replay-safe
// and rebuilds order-independent.
func EnsureIndexes() {
	col := db.GetCollection(models.RevenueFactCollection)
	if err := col.EnsureIndex(mgo.Index{
		Key: []string{"tenant_id", "source_log_id", "kind"}, Unique: true, Background: true,
	}); err != nil {
		log.Printf("analytics: facts index: %v", err)
	}
	if err := col.EnsureIndex(mgo.Index{Key: []string{"tenant_id", "occurred_at"}, Background: true}); err != nil {
		log.Printf("analytics: facts time index: %v", err)
	}
}

// AttributionWindowDays returns the configured window.
func AttributionWindowDays() int {
	if v := os.Getenv("ATTRIBUTION_WINDOW_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return DefaultAttributionWindowDays
}

// syntheticEnv marks facts/touches produced by the lifecycle harness so
// reporting can exclude them (ANA-007).
func syntheticEnv() bool { return os.Getenv("SENTANYL_E2E_MODE") == "1" }

// LooksLikeBot is the ingest-side UA heuristic (ANA-007): known crawler
// tokens and empty UAs don't create attribution touches.
func LooksLikeBot(userAgent string) bool {
	ua := strings.ToLower(userAgent)
	if ua == "" {
		return true
	}
	for _, tok := range []string{"bot", "crawler", "spider", "curl/", "wget/", "python-requests", "headless"} {
		if strings.Contains(ua, tok) {
			return true
		}
	}
	return false
}

// RecordTouch stamps a contact's last acquisition touch (funnel event or
// form submit). Bot traffic is filtered by the caller via LooksLikeBot.
func RecordTouch(tenantID, contactID bson.ObjectId, kind string, sourceID bson.ObjectId, sourceName string) {
	if contactID == "" || tenantID == "" {
		return
	}
	touch := models.LastTouch{Kind: kind, SourceID: sourceID, SourceName: sourceName, TouchedAt: time.Now()}
	if err := db.GetCollection(models.UserCollection).Update(
		bson.M{"_id": contactID, "$or": []bson.M{
			{"tenant_id": tenantID}, {"subscriber_id": tenantID.Hex()},
		}},
		bson.M{"$set": bson.M{"last_touch": touch}},
	); err != nil && err != mgo.ErrNotFound {
		log.Printf("analytics: record touch: %v", err)
	}
}

// resolveAttribution reads the contact's last touch and returns it as fact
// attribution when it falls inside the window before occurredAt.
func resolveAttribution(tenantID, contactID bson.ObjectId, occurredAt time.Time) *models.Attribution {
	if contactID == "" {
		return nil
	}
	var u models.User
	if err := db.GetCollection(models.UserCollection).FindId(contactID).One(&u); err != nil || u.LastTouch == nil {
		return nil
	}
	window := AttributionWindowDays()
	cutoff := occurredAt.Add(-time.Duration(window) * 24 * time.Hour)
	if u.LastTouch.TouchedAt.Before(cutoff) || u.LastTouch.TouchedAt.After(occurredAt.Add(time.Minute)) {
		return nil
	}
	return &models.Attribution{
		Kind: u.LastTouch.Kind, SourceID: u.LastTouch.SourceID, SourceName: u.LastTouch.SourceName,
		TouchedAt: u.LastTouch.TouchedAt, WindowDays: window,
	}
}

// WriteSaleFact projects one PurchaseLog row into an immutable sale fact.
// Idempotent by (tenant, log, kind); safe on webhook replays.
func WriteSaleFact(pl *models.PurchaseLog) {
	writeFact(pl, models.RevenueFactSale, true)
}

// WriteRefundFact writes the separate refund fact for a refunded log row.
func WriteRefundFact(pl *models.PurchaseLog) {
	writeFact(pl, models.RevenueFactRefund, false)
}

func writeFact(pl *models.PurchaseLog, kind string, attribute bool) {
	if pl == nil || pl.TenantID == "" {
		return
	}
	occurred := time.Now()
	if pl.CreatedAt != nil && kind == models.RevenueFactSale {
		occurred = *pl.CreatedAt
	}
	fact := &models.RevenueFact{
		Id:          bson.NewObjectId(),
		TenantID:    pl.TenantID,
		Kind:        kind,
		SourceLogID: pl.Id,
		AmountMinor: int64(math.Round(pl.Amount * 100)),
		Currency:    strings.ToLower(pl.Currency),
		ContactID:   pl.UserId,
		OfferID:     pl.OfferID,
		ProductID:   pl.ProductId,
		Synthetic:   syntheticEnv(),
		OccurredAt:  occurred,
		RecordedAt:  time.Now(),
	}
	if fact.Currency == "" {
		fact.Currency = "usd"
	}
	if attribute {
		fact.Attribution = resolveAttribution(pl.TenantID, pl.UserId, occurred)
	}
	if err := db.GetCollection(models.RevenueFactCollection).Insert(fact); err != nil && !mgo.IsDup(err) {
		log.Printf("analytics: write %s fact for log %s: %v", kind, pl.Id.Hex(), err)
	}
}

// RebuildFacts reprojects a tenant's facts from the purchase_logs source of
// truth. Idempotent (unique key) and order-independent: late-arriving or
// corrected logs simply gain their missing facts on the next rebuild;
// existing facts are never mutated. Returns (sales, refunds) written.
func RebuildFacts(tenantID bson.ObjectId) (int, int) {
	var logs []models.PurchaseLog
	if err := db.GetCollection(models.PurchaseLogCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).All(&logs); err != nil {
		log.Printf("analytics: rebuild facts: %v", err)
		return 0, 0
	}
	sales, refunds := 0, 0
	col := db.GetCollection(models.RevenueFactCollection)
	for i := range logs {
		pl := &logs[i]
		if n, _ := col.Find(bson.M{"tenant_id": tenantID, "source_log_id": pl.Id, "kind": models.RevenueFactSale}).Count(); n == 0 {
			WriteSaleFact(pl)
			sales++
		}
		if pl.Status == "refunded" {
			if n, _ := col.Find(bson.M{"tenant_id": tenantID, "source_log_id": pl.Id, "kind": models.RevenueFactRefund}).Count(); n == 0 {
				WriteRefundFact(pl)
				refunds++
			}
		}
	}
	return sales, refunds
}

// Reconcile compares fact totals against the purchase_logs aggregation the
// legacy endpoints use — per currency, gross and refunded — so drift between
// the projection and its source is a visible number, not a mystery.
func Reconcile(tenantID bson.ObjectId) bson.M {
	factTotals := factAggregation(tenantID)
	sourceTotals := sourceAggregation(tenantID)
	return bson.M{
		"facts":  factTotals,
		"source": sourceTotals,
		"match":  totalsEqual(factTotals, sourceTotals),
	}
}

func factAggregation(tenantID bson.ObjectId) map[string]bson.M {
	var rows []struct {
		ID struct {
			Currency string `bson:"currency"`
			Kind     string `bson:"kind"`
		} `bson:"_id"`
		Total int64 `bson:"total"`
	}
	_ = db.GetCollection(models.RevenueFactCollection).Pipe([]bson.M{
		{"$match": bson.M{"tenant_id": tenantID}},
		{"$group": bson.M{
			"_id":   bson.M{"currency": "$currency", "kind": "$kind"},
			"total": bson.M{"$sum": "$amount_minor"},
		}},
	}).All(&rows)
	out := map[string]bson.M{}
	for _, r := range rows {
		cur := out[r.ID.Currency]
		if cur == nil {
			cur = bson.M{"gross_minor": int64(0), "refunded_minor": int64(0)}
			out[r.ID.Currency] = cur
		}
		if r.ID.Kind == models.RevenueFactSale {
			cur["gross_minor"] = r.Total
		} else {
			cur["refunded_minor"] = r.Total
		}
	}
	return out
}

func sourceAggregation(tenantID bson.ObjectId) map[string]bson.M {
	var rows []struct {
		ID struct {
			Currency string `bson:"currency"`
			Status   string `bson:"status"`
		} `bson:"_id"`
		Total float64 `bson:"total"`
	}
	_ = db.GetCollection(models.PurchaseLogCollection).Pipe([]bson.M{
		{"$match": bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}},
		{"$group": bson.M{
			"_id":   bson.M{"currency": bson.M{"$toLower": bson.M{"$ifNull": []interface{}{"$currency", "usd"}}}, "status": "$status"},
			"total": bson.M{"$sum": "$amount"},
		}},
	}).All(&rows)
	out := map[string]bson.M{}
	for _, r := range rows {
		currency := r.ID.Currency
		if currency == "" {
			currency = "usd"
		}
		cur := out[currency]
		if cur == nil {
			cur = bson.M{"gross_minor": int64(0), "refunded_minor": int64(0)}
			out[currency] = cur
		}
		minor := int64(math.Round(r.Total * 100))
		cur["gross_minor"] = cur["gross_minor"].(int64) + minor
		if r.ID.Status == "refunded" {
			cur["refunded_minor"] = cur["refunded_minor"].(int64) + minor
		}
	}
	return out
}

func totalsEqual(a, b map[string]bson.M) bool {
	if len(a) != len(b) {
		return false
	}
	for cur, av := range a {
		bv, ok := b[cur]
		if !ok || av["gross_minor"] != bv["gross_minor"] || av["refunded_minor"] != bv["refunded_minor"] {
			return false
		}
	}
	return true
}

// RevenueBySource groups net revenue by attribution source (ANA-006),
// excluding synthetic traffic.
func RevenueBySource(tenantID bson.ObjectId) []bson.M {
	var rows []struct {
		ID struct {
			Kind     string `bson:"kind"`
			Source   string `bson:"source"`
			Currency string `bson:"currency"`
			FactKind string `bson:"fact_kind"`
		} `bson:"_id"`
		Total int64 `bson:"total"`
		Count int   `bson:"count"`
	}
	_ = db.GetCollection(models.RevenueFactCollection).Pipe([]bson.M{
		{"$match": bson.M{"tenant_id": tenantID, "synthetic": bson.M{"$ne": true}}},
		{"$group": bson.M{
			"_id": bson.M{
				"kind":      bson.M{"$ifNull": []interface{}{"$attribution.kind", "direct"}},
				"source":    bson.M{"$ifNull": []interface{}{"$attribution.source_name", ""}},
				"currency":  "$currency",
				"fact_kind": "$kind",
			},
			"total": bson.M{"$sum": "$amount_minor"},
			"count": bson.M{"$sum": 1},
		}},
	}).All(&rows)

	type key struct{ kind, source, currency string }
	agg := map[key]bson.M{}
	for _, r := range rows {
		k := key{r.ID.Kind, r.ID.Source, r.ID.Currency}
		cur := agg[k]
		if cur == nil {
			cur = bson.M{
				"attribution_kind": r.ID.Kind, "source_name": r.ID.Source, "currency": r.ID.Currency,
				"gross_minor": int64(0), "refunded_minor": int64(0), "net_minor": int64(0), "sales": 0,
			}
			agg[k] = cur
		}
		if r.ID.FactKind == models.RevenueFactSale {
			cur["gross_minor"] = r.Total
			cur["sales"] = r.Count
		} else {
			cur["refunded_minor"] = r.Total
		}
	}
	out := make([]bson.M, 0, len(agg))
	for _, v := range agg {
		v["net_minor"] = v["gross_minor"].(int64) - v["refunded_minor"].(int64)
		out = append(out, v)
	}
	return out
}
