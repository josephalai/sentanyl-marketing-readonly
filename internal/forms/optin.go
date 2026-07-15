package forms

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	htmlpkg "html"
	"strings"
	"time"

	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

const optInTTL = 7 * 24 * time.Hour

var ErrInvalidOptIn = errors.New("invalid or expired confirmation")

type pendingOptIn struct {
	Raw     string
	Digest  string
	Expires time.Time
}

func newPendingOptIn() pendingOptIn {
	raw := newOptInToken()
	return pendingOptIn{
		Raw:     raw,
		Digest:  digestOptInToken(raw),
		Expires: time.Now().Add(optInTTL),
	}
}

func newOptInToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return utils.GeneratePublicId() + utils.GeneratePublicId()
	}
	return hex.EncodeToString(b)
}

func digestOptInToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func isPendingOptIn(contact *pkgmodels.User) bool {
	return contact != nil && contact.ConsentOptInDigest != ""
}

// setPendingOptIn supersedes any prior raw token and associates the latest
// form submission with the deferred action chain. The contact remains
// ineligible for marketing until confirmation consumes the digest.
func setPendingOptIn(contact *pkgmodels.User, submissionID bson.ObjectId, pending pendingOptIn) error {
	if contact == nil || !submissionID.Valid() {
		return errors.New("missing pending contact or submission")
	}
	consent := false
	err := db.GetCollection(pkgmodels.UserCollection).Update(
		bson.M{"_id": contact.Id, "tenant_id": contact.TenantID},
		bson.M{"$set": bson.M{
			"subscribed":                    false,
			"consent_subscribed":            consent,
			"consent_opt_in_digest":         pending.Digest,
			"consent_opt_in_expires":        pending.Expires,
			"consent_pending_submission_id": submissionID,
			"timestamps.updated_at":         time.Now(),
		}, "$unset": bson.M{"consented_at": 1}},
	)
	if err == nil {
		contact.Subscribed = false
		contact.ConsentSubscribed = &consent
		contact.ConsentOptInDigest = pending.Digest
		contact.ConsentOptInExpires = &pending.Expires
		contact.ConsentPendingSubmissionID = submissionID
		contact.ConsentedAt = nil
	}
	return err
}

func confirmationURL(domain, raw string) string {
	domain = strings.TrimSpace(domain)
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimRight(domain, "/")
	if domain == "" {
		domain = "localhost"
	}
	scheme := "https"
	if strings.Contains(domain, "localhost") || strings.Contains(domain, "lvh.me") {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s/api/marketing/forms/confirm?token=%s", scheme, domain, raw)
}

func sendOptInConfirmation(tenantID bson.ObjectId, contact *pkgmodels.User, domain, raw string) error {
	if contact == nil || contact.Email == "" || raw == "" || contact.UnsubscribedAt != nil {
		return nil
	}
	confirmURL := confirmationURL(domain, raw)
	html := fmt.Sprintf(`<p>Please confirm that you want to hear from us.</p><p><a href="%s">Confirm your email</a></p><p style="color:#6b7280;font-size:12px">This link expires in 7 days.</p>`, htmlpkg.EscapeString(confirmURL))
	provider, from := selectEmailProvider(tenantID)
	msg := pkgmodels.NewInstantEmail()
	msg.From = from
	msg.To = string(contact.Email)
	msg.SubjectLine = "Confirm your email"
	msg.Html = html
	if err := db.GetCollection(pkgmodels.InstantEmailCollection).Insert(msg); err != nil {
		return fmt.Errorf("record confirmation email: %w", err)
	}
	if provider != nil {
		if err := provider.SendEmail(from, string(contact.Email), msg.SubjectLine, html, ""); err != nil {
			return fmt.Errorf("send confirmation email: %w", err)
		}
	}
	return nil
}

// ConfirmOptIn atomically consumes a tenant-scoped token digest, grants
// consent, and replays the deferred OnSubmit chain exactly once.
func ConfirmOptIn(tenantID bson.ObjectId, raw string) (Result, error) {
	res := Result{}
	if !tenantID.Valid() || strings.TrimSpace(raw) == "" {
		return res, ErrInvalidOptIn
	}
	now := time.Now()
	digest := digestOptInToken(raw)
	var pendingContact pkgmodels.User
	query := bson.M{
		"tenant_id":                     tenantID,
		"consent_opt_in_digest":         digest,
		"consent_opt_in_expires":        bson.M{"$gt": now},
		"consent_pending_submission_id": bson.M{"$exists": true},
		"unsubscribed_at":               nil,
	}
	if err := db.GetCollection(pkgmodels.UserCollection).Find(query).One(&pendingContact); err != nil {
		return res, ErrInvalidOptIn
	}
	consent := true
	change := mgo.Change{
		Update: bson.M{
			"$set": bson.M{
				"subscribed":            !pendingContact.LimitHeld,
				"consent_subscribed":    consent,
				"consented_at":          now,
				"timestamps.updated_at": now,
			},
			"$unset": bson.M{
				"consent_opt_in_digest":         1,
				"consent_opt_in_expires":        1,
				"consent_pending_submission_id": 1,
			},
		},
		ReturnNew: true,
	}
	var confirmed pkgmodels.User
	if _, err := db.GetCollection(pkgmodels.UserCollection).Find(query).Apply(change, &confirmed); err != nil {
		return res, ErrInvalidOptIn
	}
	res.ContactID = confirmed.Id.Hex()
	res.ContactPublicID = confirmed.PublicId

	var stored pkgmodels.FormSubmission
	if err := db.GetCollection(pkgmodels.FormSubmissionCollection).Find(bson.M{
		"_id": pendingContact.ConsentPendingSubmissionID, "tenant_id": tenantID,
	}).One(&stored); err != nil {
		res.Warnings = append(res.Warnings, "deferred submission not found")
		return res, nil
	}
	var form pkgmodels.PageForm
	if err := db.GetCollection(pkgmodels.PageFormCollection).Find(bson.M{
		"public_id": stored.FormID, "tenant_id": tenantID, "timestamps.deleted_at": nil,
	}).One(&form); err != nil {
		res.Warnings = append(res.Warnings, "deferred form not found")
		return res, nil
	}
	return runOnSubmitChain(&form, submissionFromStored(stored), &confirmed, res), nil
}

// ResendOptIn supersedes the current token for a pending contact. Callers
// deliberately hide whether a matching address exists.
func ResendOptIn(tenantID bson.ObjectId, email, domain string) error {
	var contact pkgmodels.User
	if err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
		"tenant_id":                     tenantID,
		"email":                         pkgmodels.EmailAddress(strings.ToLower(strings.TrimSpace(email))),
		"consent_opt_in_digest":         bson.M{"$ne": ""},
		"consent_pending_submission_id": bson.M{"$exists": true},
		"unsubscribed_at":               nil,
	}).One(&contact); err != nil {
		if err == mgo.ErrNotFound {
			return nil
		}
		return err
	}
	pending := newPendingOptIn()
	if err := setPendingOptIn(&contact, contact.ConsentPendingSubmissionID, pending); err != nil {
		return err
	}
	return sendOptInConfirmation(tenantID, &contact, domain, pending.Raw)
}

func submissionFromStored(stored pkgmodels.FormSubmission) Submission {
	values := make(map[string]string, len(stored.Data))
	for key, value := range stored.Data {
		values[key] = storedValueString(value)
	}
	return Submission{
		FieldValues:          values,
		Source:               stored.Source,
		VideoSessionPublicId: stored.VideoSessionPublicId,
	}
}

func storedValueString(value interface{}) string {
	switch v := value.(type) {
	case time.Time:
		return v.Format(time.RFC3339)
	case []string:
		return strings.Join(v, ",")
	case []interface{}:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			parts = append(parts, fmt.Sprint(item))
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprint(value)
	}
}
