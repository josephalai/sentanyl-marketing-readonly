// Package migration is the Kajabi migration control plane (MIG-001..005):
// parse → validate → dry-run → import → reconcile → rollback, driven by a
// MigrationProject state machine with a SourceObjectMap idempotency contract.
//
// Input is Kajabi's export files (CSV; headers matched loosely so both the
// native exports and a documented normalized format parse):
//
//	contacts.csv     — Kajabi contacts export (email required; name, tags,
//	                   email status honored)
//	transactions.csv — Kajabi payments export (transaction id, member email,
//	                   offer, amount, currency, status, date)
//	offers.csv       — optional; offers referenced by transactions are
//	                   derived as stubs when this file is absent
//	products.csv     — optional (Kajabi does NOT export products —
//	                   externally blocked; supply normalized rows to map them)
//	grants.csv       — optional; direct member→offer/product grants
//	courses.json     — optional (Kajabi does NOT export course content —
//	                   externally blocked; structure metadata only)
//	assets.csv       — optional; downloadable asset references (url,
//	                   file_name, product)
//
// Source data Kajabi does not expose is reported as externally_blocked on
// every run — never silently omitted.
package migration

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ExternallyBlocked lists Kajabi source data its exports do not provide.
// Reported verbatim on every validate/dry-run/import so the gap is explicit.
var ExternallyBlocked = []string{
	"course lesson content bodies (Kajabi provides no content export; courses.json carries structure metadata only)",
	"products catalog (no native export; supply products.csv in the normalized format or products are created as stubs from offers)",
	"website pages, funnels, and landing pages (no export; rebuild in the builder)",
	"email automations and broadcast history (no export; semantic translation impossible without source data)",
	"video/media files (no bulk export; re-upload or use assets.csv references where you host the files)",
}

// SourceContact is one parsed contacts.csv row.
type SourceContact struct {
	SourceID   string
	Email      string
	FirstName  string
	LastName   string
	Phone      string
	Tags       []string
	Subscribed bool
	// SubscribedKnown is false when the export carried no consent column —
	// the importer then defaults to subscribed but reports the assumption.
	SubscribedKnown bool
	Row             int
}

// SourceProduct is one parsed products.csv row (normalized format).
type SourceProduct struct {
	SourceID    string
	Name        string
	ProductType string
	Description string
	Row         int
}

// SourceOffer is one parsed offers.csv row, or a stub derived from a
// transaction's offer reference.
type SourceOffer struct {
	SourceID    string
	Title       string
	AmountMinor int64
	Currency    string
	ProductIDs  []string // source product ids
	Derived     bool     // built from a transaction reference, not offers.csv
	Row         int
}

// SourceTransaction is one parsed transactions.csv row.
type SourceTransaction struct {
	SourceID    string
	Email       string
	OfferRef    string // offer source id or title
	AmountMinor int64
	Currency    string
	Status      string // completed | refunded
	OccurredAt  time.Time
	Row         int
}

// SourceGrant is one parsed grants.csv row (direct entitlement, no payment).
type SourceGrant struct {
	SourceID   string
	Email      string
	OfferRef   string
	ProductRef string
	Row        int
}

// SourceCourse is one course structure entry from courses.json.
type SourceCourse struct {
	SourceID   string         `json:"id"`
	ProductRef string         `json:"product"`
	Title      string         `json:"title"`
	Modules    []SourceModule `json:"modules"`
}

// SourceModule is one module's structure metadata.
type SourceModule struct {
	Title   string   `json:"title"`
	Lessons []string `json:"lessons"`
}

// SourceAsset is one downloadable-asset reference from assets.csv.
type SourceAsset struct {
	SourceID   string
	URL        string
	FileName   string
	ProductRef string
	Row        int
}

// Export is the fully parsed input set.
type Export struct {
	Contacts     []SourceContact
	Products     []SourceProduct
	Offers       []SourceOffer
	Transactions []SourceTransaction
	Grants       []SourceGrant
	Courses      []SourceCourse
	Assets       []SourceAsset
}

// ParseError is one recoverable per-row parse failure.
type ParseError struct {
	Kind    string
	Row     int
	Message string
}

// header index resolution: first matching alias wins, case/space-insensitive.
func headerIndex(headers []string, aliases ...string) int {
	norm := func(s string) string {
		return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", "_"))
	}
	for _, a := range aliases {
		for i, h := range headers {
			if norm(h) == norm(a) {
				return i
			}
		}
	}
	return -1
}

func cell(rec []string, idx int) string {
	if idx < 0 || idx >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[idx])
}

func readCSV(content []byte) ([][]string, error) {
	r := csv.NewReader(strings.NewReader(string(content)))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	return r.ReadAll()
}

// parseAmountMinor accepts decimal major units ("49.00") or integer cents.
func parseAmountMinor(s string) (int64, error) {
	s = strings.TrimSpace(strings.TrimPrefix(s, "$"))
	s = strings.ReplaceAll(s, ",", "")
	if s == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("unparseable amount %q", s)
	}
	return int64(math.Round(f * 100)), nil
}

// ParseContacts parses a Kajabi contacts export.
func ParseContacts(content []byte) ([]SourceContact, []ParseError) {
	rows, err := readCSV(content)
	if err != nil || len(rows) == 0 {
		return nil, []ParseError{{Kind: "contacts", Message: "unreadable CSV: " + errString(err)}}
	}
	h := rows[0]
	iEmail := headerIndex(h, "email", "email_address", "member_email")
	if iEmail < 0 {
		return nil, []ParseError{{Kind: "contacts", Message: "no email column found"}}
	}
	iID := headerIndex(h, "id", "member_id", "contact_id", "external_id")
	iName := headerIndex(h, "name", "full_name")
	iFirst := headerIndex(h, "first_name")
	iLast := headerIndex(h, "last_name")
	iPhone := headerIndex(h, "phone", "phone_number")
	iTags := headerIndex(h, "tags", "tag_list")
	iStatus := headerIndex(h, "email_status", "subscription_status", "subscribed", "status")

	var out []SourceContact
	var errs []ParseError
	for n, rec := range rows[1:] {
		row := n + 2
		email := strings.ToLower(cell(rec, iEmail))
		if email == "" || !strings.Contains(email, "@") {
			errs = append(errs, ParseError{Kind: "contacts", Row: row, Message: "missing or invalid email"})
			continue
		}
		c := SourceContact{Email: email, Row: row}
		c.SourceID = cell(rec, iID)
		if c.SourceID == "" {
			c.SourceID = email
		}
		c.FirstName = cell(rec, iFirst)
		c.LastName = cell(rec, iLast)
		if c.FirstName == "" && c.LastName == "" && iName >= 0 {
			parts := strings.SplitN(cell(rec, iName), " ", 2)
			c.FirstName = parts[0]
			if len(parts) > 1 {
				c.LastName = parts[1]
			}
		}
		c.Phone = cell(rec, iPhone)
		if t := cell(rec, iTags); t != "" {
			for _, tag := range strings.Split(t, ",") {
				if tag = strings.TrimSpace(tag); tag != "" {
					c.Tags = append(c.Tags, tag)
				}
			}
		}
		if iStatus >= 0 {
			c.SubscribedKnown = true
			v := strings.ToLower(cell(rec, iStatus))
			c.Subscribed = v == "subscribed" || v == "true" || v == "active" || v == "yes"
		} else {
			// No consent column in the export: default subscribed (Kajabi
			// members opted in on the source platform) — the assumption is
			// surfaced in the report, and explicit local opt-outs always win.
			c.Subscribed = true
		}
		out = append(out, c)
	}
	return out, errs
}

// ParseProducts parses the normalized products.csv.
func ParseProducts(content []byte) ([]SourceProduct, []ParseError) {
	rows, err := readCSV(content)
	if err != nil || len(rows) == 0 {
		return nil, []ParseError{{Kind: "products", Message: "unreadable CSV: " + errString(err)}}
	}
	h := rows[0]
	iID := headerIndex(h, "id", "product_id")
	iName := headerIndex(h, "name", "title", "product")
	iType := headerIndex(h, "type", "product_type")
	iDesc := headerIndex(h, "description")
	if iName < 0 {
		return nil, []ParseError{{Kind: "products", Message: "no name column found"}}
	}
	var out []SourceProduct
	var errs []ParseError
	for n, rec := range rows[1:] {
		row := n + 2
		name := cell(rec, iName)
		if name == "" {
			errs = append(errs, ParseError{Kind: "products", Row: row, Message: "missing name"})
			continue
		}
		p := SourceProduct{Name: name, ProductType: strings.ToLower(cell(rec, iType)), Description: cell(rec, iDesc), Row: row}
		p.SourceID = cell(rec, iID)
		if p.SourceID == "" {
			p.SourceID = name
		}
		out = append(out, p)
	}
	return out, errs
}

// ParseOffers parses offers.csv.
func ParseOffers(content []byte) ([]SourceOffer, []ParseError) {
	rows, err := readCSV(content)
	if err != nil || len(rows) == 0 {
		return nil, []ParseError{{Kind: "offers", Message: "unreadable CSV: " + errString(err)}}
	}
	h := rows[0]
	iID := headerIndex(h, "id", "offer_id")
	iTitle := headerIndex(h, "title", "name", "offer")
	iAmount := headerIndex(h, "amount", "price")
	iCurrency := headerIndex(h, "currency")
	iProducts := headerIndex(h, "products", "product", "product_ids")
	if iTitle < 0 {
		return nil, []ParseError{{Kind: "offers", Message: "no title column found"}}
	}
	var out []SourceOffer
	var errs []ParseError
	for n, rec := range rows[1:] {
		row := n + 2
		title := cell(rec, iTitle)
		if title == "" {
			errs = append(errs, ParseError{Kind: "offers", Row: row, Message: "missing title"})
			continue
		}
		amount, aerr := parseAmountMinor(cell(rec, iAmount))
		if aerr != nil {
			errs = append(errs, ParseError{Kind: "offers", Row: row, Message: aerr.Error()})
			continue
		}
		o := SourceOffer{Title: title, AmountMinor: amount, Currency: strings.ToLower(cell(rec, iCurrency)), Row: row}
		o.SourceID = cell(rec, iID)
		if o.SourceID == "" {
			o.SourceID = title
		}
		if p := cell(rec, iProducts); p != "" {
			for _, id := range strings.Split(p, ",") {
				if id = strings.TrimSpace(id); id != "" {
					o.ProductIDs = append(o.ProductIDs, id)
				}
			}
		}
		out = append(out, o)
	}
	return out, errs
}

// ParseTransactions parses a Kajabi payments export.
func ParseTransactions(content []byte) ([]SourceTransaction, []ParseError) {
	rows, err := readCSV(content)
	if err != nil || len(rows) == 0 {
		return nil, []ParseError{{Kind: "transactions", Message: "unreadable CSV: " + errString(err)}}
	}
	h := rows[0]
	iID := headerIndex(h, "transaction_id", "id", "payment_id")
	iEmail := headerIndex(h, "member_email", "email", "customer_email")
	iOffer := headerIndex(h, "offer", "offer_title", "offer_id", "product")
	iAmount := headerIndex(h, "amount", "total", "price")
	iCurrency := headerIndex(h, "currency")
	iStatus := headerIndex(h, "status", "state")
	iDate := headerIndex(h, "date", "created_at", "paid_at")
	if iEmail < 0 || iOffer < 0 {
		return nil, []ParseError{{Kind: "transactions", Message: "missing member email or offer column"}}
	}
	var out []SourceTransaction
	var errs []ParseError
	for n, rec := range rows[1:] {
		row := n + 2
		email := strings.ToLower(cell(rec, iEmail))
		offer := cell(rec, iOffer)
		if email == "" || offer == "" {
			errs = append(errs, ParseError{Kind: "transactions", Row: row, Message: "missing email or offer"})
			continue
		}
		amount, aerr := parseAmountMinor(cell(rec, iAmount))
		if aerr != nil {
			errs = append(errs, ParseError{Kind: "transactions", Row: row, Message: aerr.Error()})
			continue
		}
		t := SourceTransaction{Email: email, OfferRef: offer, AmountMinor: amount, Row: row}
		t.SourceID = cell(rec, iID)
		if t.SourceID == "" {
			t.SourceID = fmt.Sprintf("%s|%s|%d", email, offer, row)
		}
		t.Currency = strings.ToLower(cell(rec, iCurrency))
		if t.Currency == "" {
			t.Currency = "usd"
		}
		switch strings.ToLower(cell(rec, iStatus)) {
		case "refunded", "refund":
			t.Status = "refunded"
		default:
			t.Status = "completed"
		}
		if d := cell(rec, iDate); d != "" {
			for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02", "01/02/2006", "January 2, 2006"} {
				if ts, perr := time.Parse(layout, d); perr == nil {
					t.OccurredAt = ts
					break
				}
			}
		}
		out = append(out, t)
	}
	return out, errs
}

// ParseGrants parses grants.csv (direct entitlements without a payment).
func ParseGrants(content []byte) ([]SourceGrant, []ParseError) {
	rows, err := readCSV(content)
	if err != nil || len(rows) == 0 {
		return nil, []ParseError{{Kind: "grants", Message: "unreadable CSV: " + errString(err)}}
	}
	h := rows[0]
	iID := headerIndex(h, "id", "grant_id")
	iEmail := headerIndex(h, "member_email", "email")
	iOffer := headerIndex(h, "offer", "offer_id")
	iProduct := headerIndex(h, "product", "product_id")
	if iEmail < 0 || (iOffer < 0 && iProduct < 0) {
		return nil, []ParseError{{Kind: "grants", Message: "missing email or offer/product column"}}
	}
	var out []SourceGrant
	var errs []ParseError
	for n, rec := range rows[1:] {
		row := n + 2
		email := strings.ToLower(cell(rec, iEmail))
		if email == "" {
			errs = append(errs, ParseError{Kind: "grants", Row: row, Message: "missing email"})
			continue
		}
		g := SourceGrant{Email: email, OfferRef: cell(rec, iOffer), ProductRef: cell(rec, iProduct), Row: row}
		if g.OfferRef == "" && g.ProductRef == "" {
			errs = append(errs, ParseError{Kind: "grants", Row: row, Message: "missing offer/product reference"})
			continue
		}
		g.SourceID = cell(rec, iID)
		if g.SourceID == "" {
			g.SourceID = email + "|" + g.OfferRef + g.ProductRef
		}
		out = append(out, g)
	}
	return out, errs
}

// ParseCourses parses courses.json (structure metadata only).
func ParseCourses(content []byte) ([]SourceCourse, []ParseError) {
	var out []SourceCourse
	if err := json.Unmarshal(content, &out); err != nil {
		return nil, []ParseError{{Kind: "courses", Message: "unreadable JSON: " + err.Error()}}
	}
	var errs []ParseError
	kept := out[:0]
	for i, c := range out {
		if c.Title == "" && c.ProductRef == "" {
			errs = append(errs, ParseError{Kind: "courses", Row: i + 1, Message: "course needs a title or product reference"})
			continue
		}
		if c.SourceID == "" {
			c.SourceID = c.ProductRef + c.Title
		}
		kept = append(kept, c)
	}
	return kept, errs
}

// ParseAssets parses assets.csv (downloadable asset references).
func ParseAssets(content []byte) ([]SourceAsset, []ParseError) {
	rows, err := readCSV(content)
	if err != nil || len(rows) == 0 {
		return nil, []ParseError{{Kind: "assets", Message: "unreadable CSV: " + errString(err)}}
	}
	h := rows[0]
	iID := headerIndex(h, "id", "asset_id")
	iURL := headerIndex(h, "url", "file_url", "download_url")
	iName := headerIndex(h, "file_name", "name", "filename")
	iProduct := headerIndex(h, "product", "product_id")
	if iURL < 0 {
		return nil, []ParseError{{Kind: "assets", Message: "no url column found"}}
	}
	var out []SourceAsset
	var errs []ParseError
	for n, rec := range rows[1:] {
		row := n + 2
		u := cell(rec, iURL)
		if u == "" {
			errs = append(errs, ParseError{Kind: "assets", Row: row, Message: "missing url"})
			continue
		}
		a := SourceAsset{URL: u, FileName: cell(rec, iName), ProductRef: cell(rec, iProduct), Row: row}
		a.SourceID = cell(rec, iID)
		if a.SourceID == "" {
			a.SourceID = u
		}
		out = append(out, a)
	}
	return out, errs
}

// DeriveOffers fills in offer stubs for transaction/grant offer references
// not covered by offers.csv, so purchases always resolve to an Offer.
func DeriveOffers(ex *Export) {
	known := map[string]bool{}
	for _, o := range ex.Offers {
		known[strings.ToLower(o.SourceID)] = true
		known[strings.ToLower(o.Title)] = true
	}
	seen := map[string]bool{}
	addRef := func(ref string, amount int64, currency string) {
		key := strings.ToLower(ref)
		if ref == "" || known[key] || seen[key] {
			return
		}
		seen[key] = true
		ex.Offers = append(ex.Offers, SourceOffer{
			SourceID: ref, Title: ref, AmountMinor: amount, Currency: currency, Derived: true,
		})
	}
	for _, t := range ex.Transactions {
		addRef(t.OfferRef, t.AmountMinor, t.Currency)
	}
	for _, g := range ex.Grants {
		addRef(g.OfferRef, 0, "")
	}
}

func errString(err error) string {
	if err == nil {
		return "empty file"
	}
	return err.Error()
}
