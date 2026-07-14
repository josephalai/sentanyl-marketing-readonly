// Package analytics is the canonical metric layer (ANA-004..008): a code-
// level metric registry, the immutable revenue_facts projection, last-touch
// attribution, and event hygiene. Reporting built here reads facts, never
// mutable operational records; the registry states each metric's definition,
// source, unit, and timezone semantics so numbers mean one thing everywhere.
package analytics

// Metric is one canonical metric definition (ANA-004). Definitions are code
// — versioned with the binary, immutable at runtime.
type Metric struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Source     string `json:"source"`
	Unit       string `json:"unit"`
	Timezone   string `json:"timezone"`
}

// Registry is the canonical metric registry. Every reporting surface that
// shows one of these numbers must compute it exactly this way.
var Registry = []Metric{
	{
		Name:       "revenue.gross",
		Definition: "Sum of sale facts' amount_minor per currency. Refunded sales remain counted here.",
		Source:     "revenue_facts kind=sale (excluding synthetic)",
		Unit:       "integer minor units per currency (never summed across currencies)",
		Timezone:   "occurred_at, bucketed by UTC calendar day",
	},
	{
		Name:       "revenue.refunded",
		Definition: "Sum of refund facts' amount_minor per currency. A refund is a separate fact, never a mutation of the sale.",
		Source:     "revenue_facts kind=refund (excluding synthetic)",
		Unit:       "integer minor units per currency",
		Timezone:   "occurred_at, bucketed by UTC calendar day",
	},
	{
		Name:       "revenue.net",
		Definition: "gross minus refunded per currency; a fully refunded sale nets to zero (never negative-counted).",
		Source:     "derived: revenue.gross − revenue.refunded",
		Unit:       "integer minor units per currency",
		Timezone:   "occurred_at, bucketed by UTC calendar day",
	},
	{
		Name:       "revenue.by_source",
		Definition: "Net revenue grouped by the last acquisition touch (funnel or form) within the attribution window preceding the sale. Sales with no touch in window group under 'direct'. The window is stamped on each fact at write time.",
		Source:     "revenue_facts.attribution (excluding synthetic)",
		Unit:       "integer minor units per currency",
		Timezone:   "occurred_at, UTC",
	},
	{
		Name:       "contacts.subscribed",
		Definition: "Count of non-deleted contacts with subscribed=true. Unsubscribed/held contacts cost nothing and are excluded.",
		Source:     "tenant_usage shared snapshot (authoritative recount on read)",
		Unit:       "count",
		Timezone:   "instantaneous",
	},
}
