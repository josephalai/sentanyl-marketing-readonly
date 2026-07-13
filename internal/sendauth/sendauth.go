// Package sendauth is the single authority every outbound-email path consults
// before sending (COM-EM-004). It centralizes what was previously scattered
// across campaign dispatch, the generic send endpoint, and the AI inbox:
// message classification, recipient routability, consent/suppression, and (as a
// hook) sender identity and quota. One decision point means suppression and
// consent policy cannot drift between channels.
package sendauth

import (
	"strings"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// Class distinguishes messages that a suppression must vs must not block.
type Class string

const (
	// Marketing sends (campaigns, newsletters, story broadcasts, AI replies)
	// are blocked by an unsubscribe/suppression.
	Marketing Class = "marketing"
	// Transactional sends (password reset, receipts, booking confirmations)
	// are not blocked by a marketing unsubscribe, but are still blocked by a
	// hard non-routable address.
	Transactional Class = "transactional"
)

// Request describes a single intended send.
type Request struct {
	TenantID bson.ObjectId
	Email    string
	Class    Class
	Purpose  string // free-form, for audit ("campaign", "password_reset", ...)
}

// Decision is the authority's ruling.
type Decision struct {
	Allowed bool
	Reason  string // machine-readable: "ok", "non_routable", "suppressed", "invalid"
}

// nonRoutableTLDs are RFC-reserved TLDs that can never receive mail. Sends to
// them (e.g. e2e fixtures like user@e2e.local) are dropped so test flows stay
// green without hard-bouncing the real MTA.
var nonRoutableTLDs = map[string]bool{
	"local": true, "localhost": true, "test": true, "invalid": true, "example": true,
}

// IsNonRoutable reports whether an address is on a reserved, undeliverable TLD.
func IsNonRoutable(email string) bool {
	if i := strings.LastIndex(email, "."); i >= 0 && i < len(email)-1 {
		return nonRoutableTLDs[strings.ToLower(email[i+1:])]
	}
	return false
}

// Authorize decides whether a single send may proceed. It never sends — callers
// send only when Decision.Allowed is true, and record Reason otherwise.
func Authorize(req Request) Decision {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		return Decision{Allowed: false, Reason: "invalid"}
	}
	if IsNonRoutable(email) {
		return Decision{Allowed: false, Reason: "non_routable"}
	}
	// Marketing class respects the contact's suppression (one-click unsubscribe,
	// bounce, complaint). Transactional class does not — a receipt or password
	// reset must still reach an unsubscribed customer.
	if req.Class == Marketing && req.TenantID != "" {
		if suppressedByAddress(req.TenantID, email) {
			return Decision{Allowed: false, Reason: "suppressed"}
		}
	}
	return Decision{Allowed: true, Reason: "ok"}
}

// suppressedByAddress reports whether any contact for this tenant with this
// email has unsubscribed. Address-scoped so a suppression on one contact row
// blocks marketing to that address regardless of which row a caller resolved.
func suppressedByAddress(tenantID bson.ObjectId, email string) bool {
	n, _ := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
		"tenant_id":       tenantID,
		"email":           pkgmodels.EmailAddress(email),
		"unsubscribed_at": bson.M{"$ne": nil},
	}).Count()
	return n > 0
}
