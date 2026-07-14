// Package deliveryverify authenticates inbound email-delivery webhooks with
// each provider's NATIVE signature scheme (COM-EM-001): a shared query-string
// secret is not an acceptable authenticity proof for suppression-driving
// events. Every adapter fails closed in production when its verification
// material is unconfigured.
package deliveryverify

import (
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/josephalai/sentanyl/pkg/auth"
)

// Verify authenticates a delivery webhook for the named provider.
// Supported: mailgun, sendgrid, postmark, powermta, generic (default).
func Verify(provider string, headers http.Header, query url.Values, body []byte) error {
	switch provider {
	case "mailgun":
		return verifyMailgun(body)
	case "sendgrid":
		return verifySendgrid(headers, body)
	case "postmark":
		return verifyPostmark(headers)
	case "powermta", "generic", "":
		return verifyShared(headers, query)
	default:
		// An unknown provider name cannot be verified natively; require the
		// platform shared secret rather than silently accepting.
		return verifyShared(headers, query)
	}
}

func failClosed(provider, envVar string) error {
	if auth.IsProductionEnv() {
		return fmt.Errorf("%s webhook verification unconfigured (%s)", provider, envVar)
	}
	return nil // dev/e2e tolerance, same posture as the shared secret
}

// verifyShared is the platform shared-secret check (header or query).
func verifyShared(headers http.Header, query url.Values) error {
	secret := os.Getenv("DELIVERY_WEBHOOK_SECRET")
	if secret == "" {
		return failClosed("delivery", "DELIVERY_WEBHOOK_SECRET")
	}
	got := headers.Get("X-Sentanyl-Delivery-Secret")
	if got == "" {
		got = query.Get("secret")
	}
	if !hmac.Equal([]byte(got), []byte(secret)) {
		return errors.New("delivery secret mismatch")
	}
	return nil
}

// verifyMailgun checks the signature block Mailgun embeds in the JSON body:
// HMAC-SHA256(timestamp + token) under the webhook signing key.
func verifyMailgun(body []byte) error {
	key := os.Getenv("MAILGUN_WEBHOOK_SIGNING_KEY")
	if key == "" {
		return failClosed("mailgun", "MAILGUN_WEBHOOK_SIGNING_KEY")
	}
	var payload struct {
		Signature struct {
			Timestamp string `json:"timestamp"`
			Token     string `json:"token"`
			Signature string `json:"signature"`
		} `json:"signature"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("mailgun payload: %w", err)
	}
	sig := payload.Signature
	if sig.Timestamp == "" || sig.Token == "" || sig.Signature == "" {
		return errors.New("mailgun signature block missing")
	}
	if ts, err := strconv.ParseInt(sig.Timestamp, 10, 64); err != nil || absDuration(time.Since(time.Unix(ts, 0))) > 15*time.Minute {
		return errors.New("mailgun timestamp outside tolerance")
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(sig.Timestamp + sig.Token))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig.Signature)) {
		return errors.New("mailgun signature mismatch")
	}
	return nil
}

// verifySendgrid checks Twilio SendGrid's Event Webhook ECDSA signature:
// base64 DER signature over (timestamp + body) under the configured base64
// DER public key.
func verifySendgrid(headers http.Header, body []byte) error {
	pubB64 := os.Getenv("SENDGRID_WEBHOOK_PUBLIC_KEY")
	if pubB64 == "" {
		return failClosed("sendgrid", "SENDGRID_WEBHOOK_PUBLIC_KEY")
	}
	sigB64 := headers.Get("X-Twilio-Email-Event-Webhook-Signature")
	ts := headers.Get("X-Twilio-Email-Event-Webhook-Timestamp")
	if sigB64 == "" || ts == "" {
		return errors.New("sendgrid signature headers missing")
	}
	der, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return fmt.Errorf("sendgrid public key: %w", err)
	}
	pubAny, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return fmt.Errorf("sendgrid public key parse: %w", err)
	}
	pub, ok := pubAny.(*ecdsa.PublicKey)
	if !ok {
		return errors.New("sendgrid public key is not ECDSA")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("sendgrid signature: %w", err)
	}
	digest := sha256.Sum256(append([]byte(ts), body...))
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		return errors.New("sendgrid signature mismatch")
	}
	return nil
}

// verifyPostmark checks the custom webhook token header (Postmark has no
// body signature; the documented pattern is a secret header or basic auth
// configured on the webhook URL).
func verifyPostmark(headers http.Header) error {
	token := os.Getenv("POSTMARK_WEBHOOK_TOKEN")
	if token == "" {
		return failClosed("postmark", "POSTMARK_WEBHOOK_TOKEN")
	}
	if !hmac.Equal([]byte(headers.Get("X-Postmark-Webhook-Token")), []byte(token)) {
		return errors.New("postmark token mismatch")
	}
	return nil
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
