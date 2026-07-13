package webhooks

import (
	"testing"

	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// WH-004: the signature is a stable HMAC over "timestamp.body" and changes with
// the secret, timestamp, or body.
func TestSignPayload(t *testing.T) {
	body := []byte(`{"id":"evt_1","type":"purchase.completed"}`)
	a := signPayload("secret", 1000, body)
	if a == "" || len(a) != 64 {
		t.Fatalf("expected 64-hex-char signature, got %q", a)
	}
	if a != signPayload("secret", 1000, body) {
		t.Fatal("signature not deterministic")
	}
	if a == signPayload("other", 1000, body) {
		t.Fatal("signature must change with secret")
	}
	if a == signPayload("secret", 1001, body) {
		t.Fatal("signature must change with timestamp")
	}
	if a == signPayload("secret", 1000, []byte(`{}`)) {
		t.Fatal("signature must change with body")
	}
}

// WH-003: subscription matching — empty list = all, wildcard, exact.
func TestSubscribed(t *testing.T) {
	all := &pkgmodels.OutboundWebhook{}
	if !subscribed(all, "purchase.completed") {
		t.Fatal("empty EventTypes should match all events")
	}
	star := &pkgmodels.OutboundWebhook{EventTypes: []string{"*"}}
	if !subscribed(star, "anything") {
		t.Fatal("wildcard should match")
	}
	exact := &pkgmodels.OutboundWebhook{EventTypes: []string{"refund.created", "purchase.completed"}}
	if !subscribed(exact, "purchase.completed") {
		t.Fatal("exact event should match")
	}
	if subscribed(exact, "course.completed") {
		t.Fatal("unsubscribed event must not match")
	}
}
