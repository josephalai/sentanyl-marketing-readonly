package forms

import (
	"strings"
	"testing"
	"time"

	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

func TestOptInTokenStoresDigestOnly(t *testing.T) {
	p := newPendingOptIn()
	if len(p.Raw) < 32 {
		t.Fatalf("raw token too short: %d", len(p.Raw))
	}
	if len(p.Digest) != 64 {
		t.Fatalf("SHA-256 digest length = %d, want 64", len(p.Digest))
	}
	if p.Digest == p.Raw || strings.Contains(p.Digest, p.Raw) {
		t.Fatal("stored digest exposes the raw token")
	}
	if got := digestOptInToken(p.Raw); got != p.Digest {
		t.Fatalf("digest mismatch: %s != %s", got, p.Digest)
	}
	if d := time.Until(p.Expires); d < optInTTL-time.Minute || d > optInTTL+time.Minute {
		t.Fatalf("expiry delta = %s, want about %s", d, optInTTL)
	}
}

func TestConfirmationURLUsesTenantDomain(t *testing.T) {
	if got := confirmationURL("localhost", "raw"); got != "http://localhost/api/marketing/forms/confirm?token=raw" {
		t.Fatalf("localhost URL = %q", got)
	}
	if got := confirmationURL("https://example.com/", "raw"); got != "https://example.com/api/marketing/forms/confirm?token=raw" {
		t.Fatalf("production URL = %q", got)
	}
}

func TestPendingOptInDetection(t *testing.T) {
	if isPendingOptIn(nil) || isPendingOptIn(&pkgmodels.User{}) {
		t.Fatal("empty contact reported pending")
	}
	if !isPendingOptIn(&pkgmodels.User{ConsentOptInDigest: strings.Repeat("a", 64)}) {
		t.Fatal("digest-bearing contact not reported pending")
	}
}

func TestStoredValueString(t *testing.T) {
	when := time.Date(2026, 7, 15, 12, 30, 0, 0, time.UTC)
	cases := []struct {
		value interface{}
		want  string
	}{
		{true, "true"},
		{12.5, "12.5"},
		{[]string{"a", "b"}, "a,b"},
		{[]interface{}{"a", "b"}, "a,b"},
		{when, "2026-07-15T12:30:00Z"},
	}
	for _, tc := range cases {
		if got := storedValueString(tc.value); got != tc.want {
			t.Errorf("storedValueString(%v) = %q, want %q", tc.value, got, tc.want)
		}
	}
}
