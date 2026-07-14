package deliveryverify

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestMailgunSignature(t *testing.T) {
	os.Setenv("MAILGUN_WEBHOOK_SIGNING_KEY", "mg-key")
	t.Cleanup(func() { os.Unsetenv("MAILGUN_WEBHOOK_SIGNING_KEY") })

	ts := fmt.Sprintf("%d", time.Now().Unix())
	token := "tok123"
	mac := hmac.New(sha256.New, []byte("mg-key"))
	mac.Write([]byte(ts + token))
	sig := hex.EncodeToString(mac.Sum(nil))
	body, _ := json.Marshal(map[string]any{
		"signature": map[string]string{"timestamp": ts, "token": token, "signature": sig},
	})
	if err := Verify("mailgun", http.Header{}, url.Values{}, body); err != nil {
		t.Fatalf("valid mailgun signature rejected: %v", err)
	}
	bad, _ := json.Marshal(map[string]any{
		"signature": map[string]string{"timestamp": ts, "token": token, "signature": "deadbeef"},
	})
	if err := Verify("mailgun", http.Header{}, url.Values{}, bad); err == nil {
		t.Fatal("forged mailgun signature accepted")
	}
	// Stale timestamp must be rejected (replay window).
	old := fmt.Sprintf("%d", time.Now().Add(-time.Hour).Unix())
	mac2 := hmac.New(sha256.New, []byte("mg-key"))
	mac2.Write([]byte(old + token))
	stale, _ := json.Marshal(map[string]any{
		"signature": map[string]string{"timestamp": old, "token": token, "signature": hex.EncodeToString(mac2.Sum(nil))},
	})
	if err := Verify("mailgun", http.Header{}, url.Values{}, stale); err == nil {
		t.Fatal("stale mailgun signature accepted")
	}
}

func TestSendgridSignature(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	os.Setenv("SENDGRID_WEBHOOK_PUBLIC_KEY", base64.StdEncoding.EncodeToString(der))
	t.Cleanup(func() { os.Unsetenv("SENDGRID_WEBHOOK_PUBLIC_KEY") })

	body := []byte(`[{"event":"bounce","email":"a@b.c"}]`)
	ts := fmt.Sprintf("%d", time.Now().Unix())
	digest := sha256.Sum256(append([]byte(ts), body...))
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set("X-Twilio-Email-Event-Webhook-Signature", base64.StdEncoding.EncodeToString(sig))
	h.Set("X-Twilio-Email-Event-Webhook-Timestamp", ts)
	if err := Verify("sendgrid", h, url.Values{}, body); err != nil {
		t.Fatalf("valid sendgrid signature rejected: %v", err)
	}
	if err := Verify("sendgrid", h, url.Values{}, []byte(`tampered`)); err == nil {
		t.Fatal("tampered sendgrid body accepted")
	}
}

func TestPostmarkToken(t *testing.T) {
	os.Setenv("POSTMARK_WEBHOOK_TOKEN", "pm-token")
	t.Cleanup(func() { os.Unsetenv("POSTMARK_WEBHOOK_TOKEN") })
	h := http.Header{}
	h.Set("X-Postmark-Webhook-Token", "pm-token")
	if err := Verify("postmark", h, url.Values{}, nil); err != nil {
		t.Fatalf("valid postmark token rejected: %v", err)
	}
	h.Set("X-Postmark-Webhook-Token", "wrong")
	if err := Verify("postmark", h, url.Values{}, nil); err == nil {
		t.Fatal("wrong postmark token accepted")
	}
}

func TestSharedSecretFallback(t *testing.T) {
	os.Setenv("DELIVERY_WEBHOOK_SECRET", "shared")
	t.Cleanup(func() { os.Unsetenv("DELIVERY_WEBHOOK_SECRET") })
	h := http.Header{}
	h.Set("X-Sentanyl-Delivery-Secret", "shared")
	if err := Verify("generic", h, url.Values{}, nil); err != nil {
		t.Fatalf("valid shared secret rejected: %v", err)
	}
	if err := Verify("unknown-provider", http.Header{}, url.Values{}, nil); err == nil {
		t.Fatal("unknown provider without secret accepted")
	}
}
