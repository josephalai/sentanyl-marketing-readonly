// Package mailoauth implements the tenant self-service mailbox OAuth flow
// (COM-EM-003): every tenant connects their own Gmail or Microsoft mailbox
// through Sentanyl's platform-level OAuth applications. The authorization-
// code flow uses PKCE and an HMAC-signed state that binds the exact
// initiating tenant + account user, so a callback can never attach a mailbox
// to a different tenant. Tokens are stored encrypted; refresh rotates them.
package mailoauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Provider names.
const (
	ProviderGmail     = "gmail"
	ProviderMicrosoft = "microsoft"
)

// Config describes one OAuth provider. Endpoints are env-overridable so the
// full flow is fixture-testable against local httptest servers; scopes are
// the MINIMUM needed to read mail and send replies.
type Config struct {
	Provider     string
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	IdentityURL  string // returns the connected mailbox's address
	RevokeURL    string
	Scopes       []string
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ForProvider returns the provider config, or an error naming the missing
// platform credential env vars (surfaced to the owner, never guessed).
func ForProvider(provider string) (*Config, error) {
	switch provider {
	case ProviderGmail:
		id, sec := os.Getenv("GOOGLE_OAUTH_CLIENT_ID"), os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET")
		if id == "" || sec == "" {
			return nil, errors.New("gmail OAuth unconfigured: set GOOGLE_OAUTH_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET")
		}
		return &Config{
			Provider:     ProviderGmail,
			ClientID:     id,
			ClientSecret: sec,
			AuthURL:      envOr("GOOGLE_OAUTH_AUTH_URL", "https://accounts.google.com/o/oauth2/v2/auth"),
			TokenURL:     envOr("GOOGLE_OAUTH_TOKEN_URL", "https://oauth2.googleapis.com/token"),
			IdentityURL:  envOr("GMAIL_IDENTITY_URL", "https://gmail.googleapis.com/gmail/v1/users/me/profile"),
			RevokeURL:    envOr("GOOGLE_OAUTH_REVOKE_URL", "https://oauth2.googleapis.com/revoke"),
			Scopes: []string{
				"https://www.googleapis.com/auth/gmail.readonly",
				"https://www.googleapis.com/auth/gmail.send",
			},
		}, nil
	case ProviderMicrosoft:
		id, sec := os.Getenv("MS_OAUTH_CLIENT_ID"), os.Getenv("MS_OAUTH_CLIENT_SECRET")
		if id == "" || sec == "" {
			return nil, errors.New("microsoft OAuth unconfigured: set MS_OAUTH_CLIENT_ID and MS_OAUTH_CLIENT_SECRET")
		}
		return &Config{
			Provider:     ProviderMicrosoft,
			ClientID:     id,
			ClientSecret: sec,
			AuthURL:      envOr("MS_OAUTH_AUTH_URL", "https://login.microsoftonline.com/common/oauth2/v2.0/authorize"),
			TokenURL:     envOr("MS_OAUTH_TOKEN_URL", "https://login.microsoftonline.com/common/oauth2/v2.0/token"),
			IdentityURL:  envOr("MSGRAPH_IDENTITY_URL", "https://graph.microsoft.com/v1.0/me"),
			RevokeURL:    "", // Microsoft has no token-revocation endpoint; disconnect deletes stored tokens
			Scopes: []string{
				"offline_access",
				"https://graph.microsoft.com/Mail.Read",
				"https://graph.microsoft.com/Mail.Send",
				"https://graph.microsoft.com/User.Read",
			},
		}, nil
	}
	return nil, fmt.Errorf("unsupported mailbox provider %q", provider)
}

// ---------- signed state + PKCE ----------

func stateSecret() []byte {
	if v := os.Getenv("MAIL_OAUTH_STATE_SECRET"); v != "" {
		return []byte(v)
	}
	return []byte(os.Getenv("JWT_SECRET"))
}

// statePayload binds the OAuth round-trip to the exact initiator. The PKCE
// verifier travels inside the signed state, so no server-side session store
// is needed and the callback can prove it belongs to this authorize request.
type statePayload struct {
	Tenant   string `json:"t"`
	User     string `json:"u"`
	Provider string `json:"p"`
	Verifier string `json:"v"`
	Exp      int64  `json:"e"`
}

// NewState mints a signed state carrying tenant/user/provider and a fresh
// PKCE verifier. Returns (state, challenge).
func NewState(tenantID, accountUserID, provider string) (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	body, err := json.Marshal(statePayload{
		Tenant: tenantID, User: accountUserID, Provider: provider,
		Verifier: verifier, Exp: time.Now().Add(15 * time.Minute).Unix(),
	})
	if err != nil {
		return "", "", err
	}
	b64 := base64.RawURLEncoding.EncodeToString(body)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return b64 + "." + signState(b64), challenge, nil
}

// VerifyState checks signature + expiry and returns the bound fields.
func VerifyState(state string) (tenantID, accountUserID, provider, verifier string, err error) {
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 {
		return "", "", "", "", errors.New("malformed state")
	}
	if !hmac.Equal([]byte(signState(parts[0])), []byte(parts[1])) {
		return "", "", "", "", errors.New("state signature mismatch")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", "", "", errors.New("state decode failed")
	}
	var p statePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", "", "", "", errors.New("state payload invalid")
	}
	if time.Now().Unix() > p.Exp {
		return "", "", "", "", errors.New("state expired")
	}
	return p.Tenant, p.User, p.Provider, p.Verifier, nil
}

func signState(b64 string) string {
	mac := hmac.New(sha256.New, stateSecret())
	mac.Write([]byte(b64))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// AuthorizeURL builds the provider consent URL for a signed state.
func AuthorizeURL(cfg *Config, redirectURI, state, challenge string) string {
	q := url.Values{}
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(cfg.Scopes, " "))
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("access_type", "offline") // Google: request a refresh token
	q.Set("prompt", "consent")      // Google: refresh token on re-consent
	return cfg.AuthURL + "?" + q.Encode()
}

// Token is the provider token response.
type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// ErrReauthRequired signals the stored authorization is dead (revoked or
// expired refresh token): the account needs the tenant to reconnect —
// callers set health state instead of crash-looping.
var ErrReauthRequired = errors.New("mailbox authorization revoked or expired; reconnect required")

// Exchange trades an authorization code (+PKCE verifier) for tokens.
func Exchange(cfg *Config, redirectURI, code, verifier string) (*Token, error) {
	form := url.Values{}
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)
	return tokenRequest(cfg.TokenURL, form)
}

// Refresh trades a refresh token for a fresh access token (and possibly a
// rotated refresh token — callers must persist a non-empty replacement).
func Refresh(cfg *Config, refreshToken string) (*Token, error) {
	form := url.Values{}
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	tok, err := tokenRequest(cfg.TokenURL, form)
	if err != nil {
		if strings.Contains(err.Error(), "invalid_grant") {
			return nil, ErrReauthRequired
		}
		return nil, err
	}
	return tok, nil
}

func tokenRequest(tokenURL string, form url.Values) (*Token, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.PostForm(tokenURL, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tok Token
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("token response: %w", err)
	}
	if tok.Error != "" {
		return nil, fmt.Errorf("token endpoint: %s (%s)", tok.Error, tok.ErrorDesc)
	}
	if resp.StatusCode >= 300 || tok.AccessToken == "" {
		return nil, fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	return &tok, nil
}

// Identity fetches the connected mailbox's canonical address so the account
// row records WHICH mailbox was authorized (never client-asserted).
func Identity(cfg *Config, accessToken string) (email string, err error) {
	req, err := http.NewRequest("GET", cfg.IdentityURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("identity endpoint returned %d", resp.StatusCode)
	}
	var payload struct {
		EmailAddress      string `json:"emailAddress"`      // gmail profile
		Mail              string `json:"mail"`              // ms graph
		UserPrincipalName string `json:"userPrincipalName"` // ms graph fallback
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	switch {
	case payload.EmailAddress != "":
		return strings.ToLower(payload.EmailAddress), nil
	case payload.Mail != "":
		return strings.ToLower(payload.Mail), nil
	case payload.UserPrincipalName != "":
		return strings.ToLower(payload.UserPrincipalName), nil
	}
	return "", errors.New("identity endpoint returned no mailbox address")
}

// Revoke best-effort revokes the token at the provider (Google supports it;
// Microsoft does not — deleting stored tokens is the disconnect there).
func Revoke(cfg *Config, token string) error {
	if cfg.RevokeURL == "" || token == "" {
		return nil
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.PostForm(cfg.RevokeURL, url.Values{"token": {token}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
