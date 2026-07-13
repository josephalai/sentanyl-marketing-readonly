package routes

import (
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

// COM-EM-001: the delivery webhook rejects requests without the shared secret
// when one is configured, and accepts a matching secret.
func TestDeliveryWebhookAuthorized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	os.Setenv("DELIVERY_WEBHOOK_SECRET", "s3cret")
	t.Cleanup(func() { os.Unsetenv("DELIVERY_WEBHOOK_SECRET") })

	newCtx := func(header string) *gin.Context {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/newsletters/webhook/mailgun", nil)
		if header != "" {
			c.Request.Header.Set("X-Sentanyl-Delivery-Secret", header)
		}
		return c
	}

	if deliveryWebhookAuthorized(newCtx("")) {
		t.Fatal("missing secret must be rejected when one is configured")
	}
	if deliveryWebhookAuthorized(newCtx("wrong")) {
		t.Fatal("wrong secret must be rejected")
	}
	if !deliveryWebhookAuthorized(newCtx("s3cret")) {
		t.Fatal("matching secret must be accepted")
	}
}
