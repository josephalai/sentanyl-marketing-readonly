package migration

import (
	"testing"
)

// MIG-002/004: parsers must map Kajabi's native headers, reject bad rows with
// per-row errors, and the consent merge must never invent or override opt-outs.

const kajabiContacts = `Name,Email,Created At,Tags,Email Status,Member ID
"Ada Lovelace",ada@example.com,2024-01-05,"vip, newsletter",subscribed,m_001
"Bob Table",bob@example.com,2024-02-06,,unsubscribed,m_002
"No Email",,2024-02-06,,subscribed,m_003
"Carol NoStatusCol",carol@example.com,2024-03-01,coaching,subscribed,m_004
`

func TestParseContactsKajabiHeaders(t *testing.T) {
	contacts, errs := ParseContacts([]byte(kajabiContacts))
	if len(contacts) != 3 {
		t.Fatalf("expected 3 contacts, got %d", len(contacts))
	}
	if len(errs) != 1 || errs[0].Row != 4 {
		t.Fatalf("expected exactly one row error at row 4, got %+v", errs)
	}
	ada := contacts[0]
	if ada.Email != "ada@example.com" || ada.SourceID != "m_001" || ada.FirstName != "Ada" || ada.LastName != "Lovelace" {
		t.Fatalf("ada parsed wrong: %+v", ada)
	}
	if len(ada.Tags) != 2 || ada.Tags[0] != "vip" || ada.Tags[1] != "newsletter" {
		t.Fatalf("ada tags wrong: %+v", ada.Tags)
	}
	if !ada.Subscribed || !ada.SubscribedKnown {
		t.Fatal("ada must be subscribed with known consent")
	}
	if contacts[1].Subscribed {
		t.Fatal("bob's source unsubscribe must be honored")
	}
}

func TestParseContactsNoConsentColumn(t *testing.T) {
	contacts, _ := ParseContacts([]byte("Email\nx@example.com\n"))
	if len(contacts) != 1 || !contacts[0].Subscribed || contacts[0].SubscribedKnown {
		t.Fatalf("no-consent-column contact should default subscribed with SubscribedKnown=false: %+v", contacts)
	}
}

const kajabiTransactions = `Transaction ID,Member Email,Offer,Amount,Currency,Status,Date
txn_1,ada@example.com,Master Course,199.00,USD,succeeded,2024-03-01
txn_2,bob@example.com,Master Course,199.00,USD,refunded,2024-04-01
txn_3,,Master Course,199.00,USD,succeeded,2024-04-02
`

func TestParseTransactions(t *testing.T) {
	txns, errs := ParseTransactions([]byte(kajabiTransactions))
	if len(txns) != 2 || len(errs) != 1 {
		t.Fatalf("expected 2 txns + 1 error, got %d/%d", len(txns), len(errs))
	}
	if txns[0].AmountMinor != 19900 || txns[0].Currency != "usd" || txns[0].Status != "completed" {
		t.Fatalf("txn_1 parsed wrong: %+v", txns[0])
	}
	if txns[1].Status != "refunded" {
		t.Fatal("refunded status must map")
	}
	if txns[0].OccurredAt.IsZero() {
		t.Fatal("date must parse")
	}
}

func TestDeriveOffers(t *testing.T) {
	ex := &Export{
		Transactions: []SourceTransaction{
			{SourceID: "t1", Email: "a@x.com", OfferRef: "Master Course", AmountMinor: 19900, Currency: "usd"},
			{SourceID: "t2", Email: "b@x.com", OfferRef: "Master Course", AmountMinor: 19900, Currency: "usd"},
		},
	}
	DeriveOffers(ex)
	if len(ex.Offers) != 1 {
		t.Fatalf("expected 1 derived offer, got %d", len(ex.Offers))
	}
	if !ex.Offers[0].Derived || ex.Offers[0].Title != "Master Course" || ex.Offers[0].AmountMinor != 19900 {
		t.Fatalf("derived offer wrong: %+v", ex.Offers[0])
	}
	// offers.csv coverage suppresses derivation.
	ex2 := &Export{
		Offers:       []SourceOffer{{SourceID: "off_1", Title: "Master Course"}},
		Transactions: ex.Transactions,
	}
	DeriveOffers(ex2)
	if len(ex2.Offers) != 1 {
		t.Fatalf("known offer must not be re-derived, got %d", len(ex2.Offers))
	}
}

func TestMergeSubscribedConsentPolicy(t *testing.T) {
	src := func(known, sub bool) SourceContact { return SourceContact{SubscribedKnown: known, Subscribed: sub} }

	if MergeSubscribed(true, false, src(true, true)) {
		t.Fatal("a local opt-out must never be overridden by an import")
	}
	if MergeSubscribed(true, true, src(true, false)) {
		t.Fatal("a source unsubscribe must be honored")
	}
	if !MergeSubscribed(true, true, src(false, true)) {
		t.Fatal("unknown source consent must preserve the local state")
	}
	if !MergeSubscribed(false, false, src(true, true)) {
		t.Fatal("new contact with explicit source opt-in imports subscribed")
	}
	if MergeSubscribed(false, false, src(true, false)) {
		t.Fatal("new contact with source unsubscribe imports unsubscribed")
	}
}

func TestParseAmountMinor(t *testing.T) {
	for in, want := range map[string]int64{"199.00": 19900, "$49.50": 4950, "1,299.99": 129999, "": 0} {
		got, err := parseAmountMinor(in)
		if err != nil || got != want {
			t.Errorf("parseAmountMinor(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := parseAmountMinor("free"); err == nil {
		t.Error("non-numeric amount must error")
	}
}

func TestParseCoursesAndAssets(t *testing.T) {
	courses, errs := ParseCourses([]byte(`[{"id":"c1","product":"Master Course","title":"Master Course","modules":[{"title":"Intro","lessons":["Welcome","Setup"]}]}]`))
	if len(errs) != 0 || len(courses) != 1 || len(courses[0].Modules) != 1 || len(courses[0].Modules[0].Lessons) != 2 {
		t.Fatalf("courses parse wrong: %+v %+v", courses, errs)
	}
	assets, errs := ParseAssets([]byte("url,file_name,product\nhttps://cdn.example.com/w.pdf,workbook.pdf,Master Course\n,missing.pdf,\n"))
	if len(assets) != 1 || len(errs) != 1 {
		t.Fatalf("assets parse wrong: %+v %+v", assets, errs)
	}
}
