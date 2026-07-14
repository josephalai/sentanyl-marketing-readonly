package mailoauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestStateRoundtripAndTamper(t *testing.T) {
	os.Setenv("MAIL_OAUTH_STATE_SECRET", "state-secret")
	t.Cleanup(func() { os.Unsetenv("MAIL_OAUTH_STATE_SECRET") })

	state, challenge, err := NewState("tenant-1", "user-1", ProviderGmail)
	if err != nil {
		t.Fatal(err)
	}
	tenant, user, provider, verifier, err := VerifyState(state)
	if err != nil || tenant != "tenant-1" || user != "user-1" || provider != ProviderGmail {
		t.Fatalf("state roundtrip failed: %v %s %s %s", err, tenant, user, provider)
	}
	// PKCE: challenge must be S256(verifier).
	sum := sha256.Sum256([]byte(verifier))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
		t.Fatal("challenge is not S256(verifier)")
	}
	if _, _, _, _, err := VerifyState(state + "x"); err == nil {
		t.Fatal("tampered state accepted")
	}
	if _, _, _, _, err := VerifyState("garbage"); err == nil {
		t.Fatal("garbage state accepted")
	}
}

// TestExchangeRefreshIdentityFixture drives the full token lifecycle against
// a local fixture provider: exchange (verifying PKCE verifier arrives),
// refresh with rotation, invalid_grant → ErrReauthRequired, identity fetch.
func TestExchangeRefreshIdentityFixture(t *testing.T) {
	var gotVerifier string
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			gotVerifier = r.Form.Get("code_verifier")
			if r.Form.Get("code") != "good-code" {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant"}`)
				return
			}
			fmt.Fprint(w, `{"access_token":"at-1","refresh_token":"rt-1","expires_in":3600}`)
		case "refresh_token":
			if r.Form.Get("refresh_token") == "rt-1" {
				fmt.Fprint(w, `{"access_token":"at-2","refresh_token":"rt-2","expires_in":3600}`)
				return
			}
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_grant","error_description":"revoked"}`)
		}
	})
	mux.HandleFunc("/profile", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"emailAddress": "Owner@Example.com"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	os.Setenv("GOOGLE_OAUTH_CLIENT_ID", "cid")
	os.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "csec")
	os.Setenv("GOOGLE_OAUTH_TOKEN_URL", srv.URL+"/token")
	os.Setenv("GMAIL_IDENTITY_URL", srv.URL+"/profile")
	t.Cleanup(func() {
		for _, k := range []string{"GOOGLE_OAUTH_CLIENT_ID", "GOOGLE_OAUTH_CLIENT_SECRET", "GOOGLE_OAUTH_TOKEN_URL", "GMAIL_IDENTITY_URL"} {
			os.Unsetenv(k)
		}
	})
	cfg, err := ForProvider(ProviderGmail)
	if err != nil {
		t.Fatal(err)
	}

	tok, err := Exchange(cfg, "https://app/callback", "good-code", "the-verifier")
	if err != nil || tok.AccessToken != "at-1" || tok.RefreshToken != "rt-1" {
		t.Fatalf("exchange failed: %v %+v", err, tok)
	}
	if gotVerifier != "the-verifier" {
		t.Fatal("PKCE verifier did not reach the token endpoint")
	}

	email, err := Identity(cfg, tok.AccessToken)
	if err != nil || email != "owner@example.com" {
		t.Fatalf("identity failed: %v %q", err, email)
	}

	rot, err := Refresh(cfg, "rt-1")
	if err != nil || rot.AccessToken != "at-2" || rot.RefreshToken != "rt-2" {
		t.Fatalf("refresh rotation failed: %v %+v", err, rot)
	}
	if _, err := Refresh(cfg, "rt-dead"); err != ErrReauthRequired {
		t.Fatalf("revoked refresh must map to ErrReauthRequired, got %v", err)
	}
}

func TestAuthorizeURLShape(t *testing.T) {
	os.Setenv("GOOGLE_OAUTH_CLIENT_ID", "cid")
	os.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "csec")
	t.Cleanup(func() { os.Unsetenv("GOOGLE_OAUTH_CLIENT_ID"); os.Unsetenv("GOOGLE_OAUTH_CLIENT_SECRET") })
	cfg, _ := ForProvider(ProviderGmail)
	u := AuthorizeURL(cfg, "https://app/cb", "STATE", "CHALLENGE")
	for _, want := range []string{"client_id=cid", "state=STATE", "code_challenge=CHALLENGE", "code_challenge_method=S256", "gmail.readonly"} {
		if !strings.Contains(u, want) {
			t.Fatalf("authorize url missing %q: %s", want, u)
		}
	}
}
