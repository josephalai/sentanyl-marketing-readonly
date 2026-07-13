package sendauth

import "testing"

// COM-EM-004: routability and validity are enforced without touching the DB.
func TestAuthorizeRoutability(t *testing.T) {
	if d := Authorize(Request{Email: "user@e2e.local", Class: Transactional}); d.Allowed || d.Reason != "non_routable" {
		t.Fatalf("reserved TLD should be non_routable, got %+v", d)
	}
	if d := Authorize(Request{Email: "bob@host.test", Class: Transactional}); d.Allowed || d.Reason != "non_routable" {
		t.Fatalf("host.test should be non_routable, got %+v", d)
	}
	if d := Authorize(Request{Email: "not-an-email", Class: Transactional}); d.Allowed || d.Reason != "invalid" {
		t.Fatalf("malformed address should be invalid, got %+v", d)
	}
	if d := Authorize(Request{Email: "", Class: Marketing}); d.Allowed {
		t.Fatal("empty address must be denied")
	}
	// A routable transactional send with no tenant (no suppression lookup) is
	// allowed — transactional never blocks on marketing suppression.
	if d := Authorize(Request{Email: "real@example.com", Class: Transactional}); !d.Allowed {
		t.Fatalf("routable transactional send should be allowed, got %+v", d)
	}
}

func TestIsNonRoutable(t *testing.T) {
	for _, e := range []string{"a@x.local", "a@y.test", "a@z.invalid", "a@q.example", "a@w.localhost"} {
		if !IsNonRoutable(e) {
			t.Errorf("%s should be non-routable", e)
		}
	}
	for _, e := range []string{"a@example.com", "a@sentanyl.com", "a@gmail.com"} {
		if IsNonRoutable(e) {
			t.Errorf("%s should be routable", e)
		}
	}
}
