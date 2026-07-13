package routes

import (
	"testing"
	"time"

	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

func boolPtr(b bool) *bool { return &b }

func activeAgent() *pkgmodels.InboxAgent {
	return &pkgmodels.InboxAgent{Status: pkgmodels.InboxAgentStatusActive}
}

func TestBlockingGuardrailOrder(t *testing.T) {
	now := time.Now()
	unsubbed := time.Now()
	tenantOff := &pkgmodels.Tenant{InboxAutoRespondEnabled: boolPtr(false)}
	tenantOn := &pkgmodels.Tenant{}

	// Suppression outranks everything, including the master toggle.
	got := blockingGuardrail(tenantOff, activeAgent(), &pkgmodels.User{UnsubscribedAt: &unsubbed}, "sales_inquiry", now)
	if got != "contact_unsubscribed" {
		t.Fatalf("suppressed contact: got %q", got)
	}

	if got := blockingGuardrail(tenantOff, activeAgent(), &pkgmodels.User{}, "sales_inquiry", now); got != "tenant_auto_respond_disabled" {
		t.Fatalf("master toggle: got %q", got)
	}

	paused := activeAgent()
	paused.Status = pkgmodels.InboxAgentStatusPaused
	if got := blockingGuardrail(tenantOn, paused, &pkgmodels.User{}, "sales_inquiry", now); got != "agent_paused" {
		t.Fatalf("paused agent: got %q", got)
	}

	blocked := activeAgent()
	blocked.BlockedCategories = []string{"refund_or_cancellation"}
	if got := blockingGuardrail(tenantOn, blocked, &pkgmodels.User{}, "refund_or_cancellation", now); got != "blocked_category:refund_or_cancellation" {
		t.Fatalf("blocked category: got %q", got)
	}

	if got := blockingGuardrail(tenantOn, activeAgent(), &pkgmodels.User{}, "sales_inquiry", now); got != "" {
		t.Fatalf("clean path: got %q", got)
	}
}

func TestBlockedGuardrailAction(t *testing.T) {
	cases := map[string]string{
		"contact_unsubscribed":         "escalate",
		"blocked_category:complaint":   "escalate",
		"tenant_auto_respond_disabled": "save_draft",
		"agent_paused":                 "save_draft",
		"daily_cap":                    "save_draft",
	}
	for reason, want := range cases {
		if got := blockedGuardrailAction(reason); got != want {
			t.Fatalf("%s: got %q want %q", reason, got, want)
		}
	}
}

func TestNextSendWindowOpen(t *testing.T) {
	agent := activeAgent()
	agent.QuietHoursStart = "08:00"
	agent.QuietHoursEnd = "20:00"

	inside := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	if _, outside := nextSendWindowOpen(agent, inside); outside {
		t.Fatal("noon should be inside an 08-20 window")
	}
	night := time.Date(2026, 7, 13, 22, 0, 0, 0, time.UTC)
	opens, outside := nextSendWindowOpen(agent, night)
	if !outside {
		t.Fatal("22:00 should be outside an 08-20 window")
	}
	if opens.Hour() != 8 || !opens.After(night) {
		t.Fatalf("window should open at the next 08:00, got %v", opens)
	}

	// Overnight window (22:00–06:00).
	agent.QuietHoursStart = "22:00"
	agent.QuietHoursEnd = "06:00"
	if _, outside := nextSendWindowOpen(agent, night); outside {
		t.Fatal("22:00 should be inside a 22-06 window")
	}
	if _, outside := nextSendWindowOpen(agent, inside); !outside {
		t.Fatal("noon should be outside a 22-06 window")
	}

	// Unset window: always allowed.
	agent.QuietHoursStart, agent.QuietHoursEnd = "", ""
	if _, outside := nextSendWindowOpen(agent, night); outside {
		t.Fatal("unset window must always allow")
	}
}

func TestValidateInboxAgentUpdate(t *testing.T) {
	if msg := validateInboxAgentUpdate(map[string]interface{}{"confidence_threshold": 1.5}); msg == "" {
		t.Fatal("threshold > 1 should fail")
	}
	if msg := validateInboxAgentUpdate(map[string]interface{}{"quiet_hours_start": "25:00"}); msg == "" {
		t.Fatal("bad clock should fail")
	}
	if msg := validateInboxAgentUpdate(map[string]interface{}{"timezone": "Not/AZone"}); msg == "" {
		t.Fatal("bad timezone should fail")
	}
	if msg := validateInboxAgentUpdate(map[string]interface{}{"blocked_categories": []interface{}{"not_a_category"}}); msg == "" {
		t.Fatal("unknown category should fail")
	}
	if msg := validateInboxAgentUpdate(map[string]interface{}{"tool_whitelist": []interface{}{"not_a_tool"}}); msg == "" {
		t.Fatal("unknown tool should fail")
	}
	update := map[string]interface{}{
		"confidence_threshold":            0.8,
		"max_replies_per_contact_per_day": 2.0,
		"quiet_hours_start":               "08:00",
		"quiet_hours_end":                 "20:00",
		"timezone":                        "America/New_York",
		"blocked_categories":              []interface{}{"complaint"},
		"tool_whitelist":                  []interface{}{"todos_create"},
	}
	if msg := validateInboxAgentUpdate(update); msg != "" {
		t.Fatalf("valid update rejected: %s", msg)
	}
	if v, ok := update["max_replies_per_contact_per_day"].(int); !ok || v != 2 {
		t.Fatalf("daily cap not coerced to int: %#v", update["max_replies_per_contact_per_day"])
	}
}
