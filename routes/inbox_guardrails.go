package routes

import (
	"fmt"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/mcptools"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// inboxCategorySet is the canonical classifier vocabulary (union of the AI and
// keyword classifiers). Agent blocklists are validated against it.
var inboxCategorySet = map[string]bool{
	"sales_inquiry":          true,
	"objection":              true,
	"pricing_question":       true,
	"support_request":        true,
	"login_access_issue":     true,
	"refund_request":         true,
	"cancellation_request":   true,
	"refund_or_cancellation": true,
	"complaint":              true,
	"testimonial":            true,
	"partnership_inquiry":    true,
	"general_inquiry":        true,
	"spam":                   true,
}

// guardrailVerdict is the outcome of the safety evaluation for one draft.
// Action mirrors decideInboxAction's vocabulary (escalate | save_draft |
// timer_send | auto_send); Reason is set whenever a guardrail overrode the
// agent's autonomy ladder, for the activity log.
type guardrailVerdict struct {
	Action      string
	Reason      string
	SendAfterAt *time.Time
}

// evaluateSendGuardrails applies the full guardrail ladder at draft-decision
// time. First hit wins:
//  1. suppression (non-disableable)  → escalate
//  2. tenant master toggle off       → save_draft
//  3. agent paused                   → save_draft
//  4. blocked category               → escalate
//  5. per-contact daily cap          → save_draft
//  6. confidence/risk autonomy ladder (agent threshold)
//  7. quiet hours: auto_send outside the send window queues as timer_send
func evaluateSendGuardrails(tenant *pkgmodels.Tenant, agent *pkgmodels.InboxAgent, contact *pkgmodels.User, c inboxClassification, now time.Time) guardrailVerdict {
	if reason := blockingGuardrail(tenant, agent, contact, c.PrimaryCategory, now); reason != "" {
		return guardrailVerdict{Action: blockedGuardrailAction(reason), Reason: reason}
	}
	action := decideInboxAction(agent, c)
	if action == "auto_send" {
		if next, outside := nextSendWindowOpen(agent, now); outside {
			return guardrailVerdict{Action: "timer_send", Reason: "quiet_hours", SendAfterAt: &next}
		}
	}
	return guardrailVerdict{Action: action}
}

// recheckSendGuardrails re-runs the blocking guardrails (steps 1–5) against a
// stored draft at send time — the timer loop and send endpoints call this so a
// contact who unsubscribed (or a tenant who flipped the master switch) after
// the draft was queued is still protected. Returns "" when the send may
// proceed.
func recheckSendGuardrails(agent *pkgmodels.InboxAgent, draft *pkgmodels.AIReplyDraft, now time.Time) string {
	tenant := loadTenantForInbox(draft.TenantID)
	var contact pkgmodels.User
	_ = db.GetCollection(pkgmodels.UserCollection).FindId(draft.ContactID).One(&contact)
	return blockingGuardrail(tenant, agent, &contact, draft.Category, now)
}

// blockingGuardrail evaluates guardrails 1–5 and returns the machine-readable
// reason of the first violation, or "" when none apply.
func blockingGuardrail(tenant *pkgmodels.Tenant, agent *pkgmodels.InboxAgent, contact *pkgmodels.User, category string, now time.Time) string {
	if contact.IsSuppressed() {
		return "contact_unsubscribed"
	}
	if !tenant.InboxAutoRespond() {
		return "tenant_auto_respond_disabled"
	}
	if agent.Status != pkgmodels.InboxAgentStatusActive {
		return "agent_paused"
	}
	for _, blocked := range agent.BlockedCategories {
		if blocked == category {
			return "blocked_category:" + category
		}
	}
	if agent.MaxRepliesPerContactPerDay > 0 && contact.Id.Valid() {
		if countAIRepliesSentToday(agent, contact.Id, now) >= agent.MaxRepliesPerContactPerDay {
			return "daily_cap"
		}
	}
	return ""
}

// blockedGuardrailAction maps a violation reason to the draft disposition.
// Suppressed contacts and blocked categories need a human (escalate); the
// rest simply hold the draft.
func blockedGuardrailAction(reason string) string {
	if reason == "contact_unsubscribed" || len(reason) > len("blocked_category:") && reason[:len("blocked_category:")] == "blocked_category:" {
		return "escalate"
	}
	return "save_draft"
}

func loadTenantForInbox(tenantID bson.ObjectId) *pkgmodels.Tenant {
	var tenant pkgmodels.Tenant
	if err := db.GetCollection(pkgmodels.TenantCollection).FindId(tenantID).One(&tenant); err != nil {
		return nil
	}
	return &tenant
}

func countAIRepliesSentToday(agent *pkgmodels.InboxAgent, contactID bson.ObjectId, now time.Time) int {
	loc := agentLocation(agent)
	local := now.In(loc)
	startOfDay := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	n, err := db.GetCollection(pkgmodels.AIReplyDraftCollection).Find(bson.M{
		"tenant_id":             agent.TenantID,
		"contact_id":            contactID,
		"status":                pkgmodels.AIReplyDraftStatusSent,
		"sent_at":               bson.M{"$gte": startOfDay},
		"timestamps.deleted_at": nil,
	}).Count()
	if err != nil {
		// Fail closed: an unreadable counter must not unlock unlimited sends.
		return agent.MaxRepliesPerContactPerDay
	}
	return n
}

func agentLocation(agent *pkgmodels.InboxAgent) *time.Location {
	if agent.Timezone != "" {
		if loc, err := time.LoadLocation(agent.Timezone); err == nil {
			return loc
		}
	}
	return time.UTC
}

// nextSendWindowOpen reports whether now falls outside the agent's allowed
// send window (QuietHoursStart–QuietHoursEnd, agent timezone) and, if so, when
// the window next opens. Windows may cross midnight (e.g. 22:00–06:00). An
// unset or degenerate window means sending is always allowed.
func nextSendWindowOpen(agent *pkgmodels.InboxAgent, now time.Time) (time.Time, bool) {
	start, okS := parseClock(agent.QuietHoursStart)
	end, okE := parseClock(agent.QuietHoursEnd)
	if !okS || !okE || start == end {
		return time.Time{}, false
	}
	loc := agentLocation(agent)
	local := now.In(loc)
	minutes := local.Hour()*60 + local.Minute()
	inWindow := false
	if start < end {
		inWindow = minutes >= start && minutes < end
	} else {
		inWindow = minutes >= start || minutes < end
	}
	if inWindow {
		return time.Time{}, false
	}
	opens := time.Date(local.Year(), local.Month(), local.Day(), start/60, start%60, 0, 0, loc)
	if !opens.After(local) {
		opens = opens.Add(24 * time.Hour)
	}
	return opens, true
}

// validateInboxAgentUpdate sanity-checks (and normalizes in place) the
// guardrail fields of a PUT /inbox-agents/:id update. Values arrive as raw
// JSON-decoded types (float64, []interface{}). Returns "" when valid.
func validateInboxAgentUpdate(update bson.M) string {
	if v, ok := update["confidence_threshold"]; ok {
		f, isNum := v.(float64)
		if !isNum || f < 0 || f > 1 {
			return "confidence_threshold must be a number between 0 and 1"
		}
	}
	if v, ok := update["max_replies_per_contact_per_day"]; ok {
		f, isNum := v.(float64)
		if !isNum || f < 0 || f != float64(int(f)) {
			return "max_replies_per_contact_per_day must be a non-negative integer"
		}
		update["max_replies_per_contact_per_day"] = int(f)
	}
	for _, k := range []string{"quiet_hours_start", "quiet_hours_end"} {
		if v, ok := update[k]; ok {
			s, isStr := v.(string)
			if !isStr {
				return k + " must be a string"
			}
			if _, valid := parseClock(s); s != "" && !valid {
				return k + " must be HH:MM"
			}
		}
	}
	if v, ok := update["timezone"]; ok {
		s, isStr := v.(string)
		if !isStr {
			return "timezone must be a string"
		}
		if s != "" {
			if _, err := time.LoadLocation(s); err != nil {
				return "timezone must be a valid IANA timezone"
			}
		}
	}
	if v, ok := update["blocked_categories"]; ok {
		cats, errMsg := toStringSlice(v, "blocked_categories")
		if errMsg != "" {
			return errMsg
		}
		for _, cat := range cats {
			if !inboxCategorySet[cat] {
				return "unknown category: " + cat
			}
		}
		update["blocked_categories"] = cats
	}
	if v, ok := update["tool_whitelist"]; ok {
		tools, errMsg := toStringSlice(v, "tool_whitelist")
		if errMsg != "" {
			return errMsg
		}
		for _, name := range tools {
			if mcptools.Find(name) == nil {
				return "unknown tool: " + name
			}
		}
		update["tool_whitelist"] = tools
	}
	return ""
}

func toStringSlice(v interface{}, field string) ([]string, string) {
	raw, isSlice := v.([]interface{})
	if !isSlice {
		return nil, field + " must be an array of strings"
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, isStr := item.(string)
		if !isStr {
			return nil, field + " must be an array of strings"
		}
		out = append(out, s)
	}
	return out, ""
}

// parseClock parses "HH:MM" into minutes past midnight.
func parseClock(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
