// Package webhooks delivers tenant outbound webhooks durably (WH-003) with a
// versioned, signed payload (WH-004) through the SSRF-safe egress boundary
// (WH-002), using the platform job kernel for retry and dead-lettering.
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/egress"
	"github.com/josephalai/sentanyl/pkg/jobs"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// JobTypeDelivery is the durable job type for one webhook delivery.
const JobTypeDelivery = "webhook.delivery"

// payloadVersion is the outbound event contract version (WH-004).
const payloadVersion = 1

// SignatureHeader carries the timestamped HMAC signature.
const SignatureHeader = "X-Sentanyl-Signature"

// RegisterHandlers wires the delivery handler into the job kernel. Call at
// startup in the service that runs the job worker.
func RegisterHandlers() {
	jobs.Register(JobTypeDelivery, deliver)
}

// Emit enqueues a durable delivery job for every active webhook in the tenant
// subscribed to eventType (an empty EventTypes list means "all events"). The
// event id makes each (event, webhook) delivery idempotent so a producer that
// retries does not double-send.
func Emit(tenantID bson.ObjectId, eventType string, data map[string]interface{}) error {
	eventID := utils.GeneratePublicId()
	var hooks []pkgmodels.OutboundWebhook
	if err := db.GetCollection(pkgmodels.OutboundWebhookCollection).Find(bson.M{
		"subscriber_id":         tenantID.Hex(),
		"active":                true,
		"timestamps.deleted_at": bson.M{"$exists": false},
	}).All(&hooks); err != nil {
		return err
	}
	for i := range hooks {
		h := &hooks[i]
		if !subscribed(h, eventType) {
			continue
		}
		payload := bson.M{
			"id":         eventID,
			"type":       eventType,
			"version":    payloadVersion,
			"created_at": time.Now().UTC().Format(time.RFC3339),
			"data":       data,
		}
		job := jobs.NewJob(JobTypeDelivery, eventID+":"+h.PublicId,
			jobs.Envelope{TenantID: tenantID, Actor: "system", Subject: "webhook:" + h.PublicId, Version: payloadVersion, CorrelationID: eventID},
			bson.M{"webhook_id": h.PublicId, "payload": payload},
		)
		if err := jobs.Enqueue(job); err != nil {
			return err
		}
	}
	return nil
}

func subscribed(h *pkgmodels.OutboundWebhook, eventType string) bool {
	if len(h.EventTypes) == 0 {
		return true
	}
	for _, t := range h.EventTypes {
		if t == eventType || t == "*" {
			return true
		}
	}
	return false
}

// deliver executes one webhook delivery job. A non-2xx response or transport
// error returns an error so the job kernel retries with backoff; exhausted
// deliveries dead-letter for the operator console.
func deliver(ctx context.Context, job *jobs.Job) error {
	webhookID, _ := job.Payload["webhook_id"].(string)
	if webhookID == "" {
		return fmt.Errorf("delivery job missing webhook_id")
	}
	var hook pkgmodels.OutboundWebhook
	if err := db.GetCollection(pkgmodels.OutboundWebhookCollection).Find(bson.M{
		"subscriber_id": job.TenantID.Hex(),
		"public_id":     webhookID,
		"active":        true,
	}).One(&hook); err != nil {
		// Webhook removed/deactivated since enqueue — nothing to deliver.
		return nil
	}
	body, err := json.Marshal(job.Payload["payload"])
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	target, err := egress.ValidateURL(hook.URL)
	if err != nil {
		return fmt.Errorf("unsafe webhook url: %w", err)
	}

	ts := time.Now().Unix()
	secret := utils.DecryptSecret(hook.Secret)
	sig := signPayload(secret, ts, body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SignatureHeader, fmt.Sprintf("t=%d,v1=%s", ts, sig))
	req.Header.Set("X-Sentanyl-Event", fmt.Sprintf("%v", job.Payload["payload"].(bson.M)["type"]))

	resp, err := egress.SafeClient().Do(req)
	if err != nil {
		return fmt.Errorf("deliver: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("endpoint returned %d", resp.StatusCode)
	}
	return nil
}

// signPayload computes the HMAC-SHA256 signature over "timestamp.body".
func signPayload(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
