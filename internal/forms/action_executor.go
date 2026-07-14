// Package forms hosts the action executor that runs when a public visitor
// submits a PageForm. The executor walks the form's OnSubmit declaration
// (assign badges, add to lists, start storylines, deliver downloads, …)
// against canonical Mongo collections, mirroring the patterns established by
// marketing-service/routes/funnel.go's executeFunnelAction.
package forms

import (
	"fmt"
	htmlpkg "html"
	"log"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/email"
	"github.com/josephalai/sentanyl/marketing-service/internal/analytics"
	"github.com/josephalai/sentanyl/marketing-service/routes"
	"github.com/josephalai/sentanyl/pkg/badges"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/plans"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// downloadEmailTTL is how long the signed URLs in a lead-magnet email stay
// valid. Long enough that a recipient can open the email an hour later and
// still grab the file; short enough that the URLs aren't useful as a
// permanent share. The customer-portal still uses 60s URLs (mint-on-click).
const downloadEmailTTL = 24 * time.Hour

// Submission is the validated, normalized input the executor consumes.
// FieldValues is keyed by FormField.FieldName as configured on the form.
type Submission struct {
	FieldValues map[string]string
	// Source records which public surface the submission came from:
	// "builder_page" (published funnel/site pages) or "coded_embed"
	// (BYO-website channel embeds). Stored on the FormSubmission row.
	Source string
	// VideoSessionPublicId, when set, stamps any PurchaseLog rows the
	// executor creates (e.g. granted_via_form rows) so a video-driven
	// landing page's bottom-of-page conversion is attributable to the
	// watch session that drove it. Populated by the public form-submit
	// handler from the request body's video_session_id field.
	VideoSessionPublicId string
}

// Result is what the executor returns to the public submit handler.
// Warnings collect non-fatal step failures so the caller can log without
// aborting the chain.
type Result struct {
	ContactID        string                  `json:"contact_id,omitempty"`
	ContactPublicID  string                  `json:"contact_public_id,omitempty"`
	BadgesAssigned   []string                `json:"badges_assigned,omitempty"`
	BadgesRemoved    []string                `json:"badges_removed,omitempty"`
	ListsAdded       []string                `json:"lists_added,omitempty"`
	ListsRemoved     []string                `json:"lists_removed,omitempty"`
	StoriesStarted   []string                `json:"stories_started,omitempty"`
	Downloads        []routes.SignedDownload `json:"downloads,omitempty"`
	ProductsGranted  []string                `json:"products_granted,omitempty"`
	OfferAttached    string                  `json:"offer_attached,omitempty"`
	RedirectURL      string                  `json:"redirect_url,omitempty"`
	Warnings         []string                `json:"warnings,omitempty"`
}

// Execute runs the full action chain for the given form + submission.
// It never returns an error for downstream-action failures — those go into
// Result.Warnings so a single misconfigured action doesn't abort the rest of
// the chain. A nil/missing Contact terminates early because every action
// targets a contact.
func Execute(form *pkgmodels.PageForm, sub Submission) Result {
	res := Result{}
	if form == nil {
		res.Warnings = append(res.Warnings, "form not found")
		return res
	}

	res.Warnings = append(res.Warnings, validateOptions(form, sub.FieldValues)...)

	contact, err := upsertContact(form, sub)
	if err != nil || contact == nil {
		if err != nil {
			res.Warnings = append(res.Warnings, "upsert_contact: "+err.Error())
		} else {
			res.Warnings = append(res.Warnings, "upsert_contact: missing email")
		}
		// Keep the raw answers even when no contact could be resolved.
		recordSubmission(form, sub, nil)
		return res
	}
	res.ContactID = contact.Id.Hex()
	res.ContactPublicID = contact.PublicId
	recordSubmission(form, sub, contact)
	// ANA-006: a form submit is an acquisition touch for revenue attribution.
	analytics.RecordTouch(form.TenantID, contact.Id, "form", form.Id, form.Name)

	on := form.OnSubmit
	if on == nil {
		// No declared chain — but we still upserted, which is the implicit
		// "lead capture only" behavior the original public submit endpoint
		// provided. Return what we have.
		return res
	}

	if on.WriteAttributes {
		writeAttributes(form, sub, contact)
	}

	for _, badgePub := range on.AssignBadgeIds {
		if name, ok := assignBadge(form.TenantID, contact.Id, badgePub); ok {
			res.BadgesAssigned = append(res.BadgesAssigned, name)
		} else {
			res.Warnings = append(res.Warnings, "assign_badge: "+badgePub+" not found")
		}
	}
	for _, badgePub := range on.RemoveBadgeIds {
		if name, ok := removeBadge(form.TenantID, contact.Id, badgePub); ok {
			res.BadgesRemoved = append(res.BadgesRemoved, name)
		} else {
			res.Warnings = append(res.Warnings, "remove_badge: "+badgePub+" not found")
		}
	}

	for _, listPub := range on.AddToListIds {
		if name, ok := addToList(form.TenantID, contact.Id, listPub); ok {
			res.ListsAdded = append(res.ListsAdded, name)
		} else {
			res.Warnings = append(res.Warnings, "add_to_list: "+listPub+" not found")
		}
	}
	for _, listPub := range on.RemoveFromListIds {
		if name, ok := removeFromList(form.TenantID, contact.Id, listPub); ok {
			res.ListsRemoved = append(res.ListsRemoved, name)
		} else {
			res.Warnings = append(res.Warnings, "remove_from_list: "+listPub+" not found")
		}
	}

	for _, storyPub := range on.StartStoryIds {
		if name, ok := startStory(form.TenantID, contact.PublicId, storyPub); ok {
			res.StoriesStarted = append(res.StoriesStarted, name)
		} else {
			res.Warnings = append(res.Warnings, "start_story: "+storyPub+" not found")
		}
	}

	for _, productPub := range on.DeliverDownloadIds {
		// 24h TTL because we send these URLs in an email — short enough that
		// they don't function as permanent shares but long enough that the
		// recipient can open the message later and still download.
		signed, sErr := routes.SignProductDownloads(form.TenantID, productPub, downloadEmailTTL)
		if sErr != nil {
			res.Warnings = append(res.Warnings, "deliver_download: "+sErr.Error())
			continue
		}
		res.Downloads = append(res.Downloads, signed...)
	}

	// Email the signed URLs so form-encoded submissions (which 303-redirect
	// past the JSON response) still get the download. JSON callers also
	// receive the URLs inline in Result.Downloads. Best-effort — a missing
	// SMTP provider just skips the email rather than blocking the chain.
	if len(res.Downloads) > 0 {
		if err := sendDownloadDeliveryEmail(form.TenantID, contact, res.Downloads); err != nil {
			res.Warnings = append(res.Warnings, "deliver_download_email: "+err.Error())
		}
	}

	for _, productPub := range on.GrantProductIds {
		if ok := grantProductAccess(form.TenantID, contact, productPub, sub.VideoSessionPublicId); ok {
			res.ProductsGranted = append(res.ProductsGranted, productPub)
		} else {
			res.Warnings = append(res.Warnings, "grant_product: "+productPub+" failed")
		}
	}

	if on.AttachOfferId != "" {
		// AttachOfferId is recorded on the result for the caller to use as a
		// post-submit checkout hint. Stripe checkout itself runs through the
		// existing /api/marketing/site/checkout/start path, which the form
		// page can call after the submit succeeds.
		res.OfferAttached = on.AttachOfferId
	}

	res.RedirectURL = strings.TrimSpace(on.RedirectURL)

	return res
}

// ── contact upsert ────────────────────────────────────────────────────────

func upsertContact(form *pkgmodels.PageForm, sub Submission) (*pkgmodels.User, error) {
	email := strings.ToLower(strings.TrimSpace(emailFromSubmission(form, sub)))
	if email == "" {
		return nil, nil
	}
	col := db.GetCollection(pkgmodels.UserCollection)
	var existing pkgmodels.User
	err := col.Find(bson.M{
		"email":     pkgmodels.EmailAddress(email),
		"tenant_id": form.TenantID,
	}).One(&existing)
	if err == nil {
		// Backfill SubscriberId on legacy rows (the modern stack stopped
		// writing it; the story engine still requires it). One-shot
		// $set, no-op when already set, so this is safe to run on every
		// match.
		if existing.SubscriberId == "" {
			tenantHex := form.TenantID.Hex()
			_ = col.Update(
				bson.M{"_id": existing.Id},
				bson.M{"$set": bson.M{"subscriber_id": tenantHex}},
			)
			existing.SubscriberId = tenantHex
		}
		return &existing, nil
	}

	now := time.Now()
	contact := pkgmodels.User{
		Id:       bson.NewObjectId(),
		PublicId: utils.GeneratePublicId(),
		TenantID: form.TenantID,
		// SubscriberId is the legacy tenant-scope key the story engine
		// (core-service/routes/story_engine.go StartStoryForUser) and
		// triggerStoryStart still read. Without this set, storyline
		// dispatch silently no-ops because TriggerStoryStart can't
		// resolve a non-empty subscriber_id from the new contact.
		SubscriberId: form.TenantID.Hex(),
		Email:        pkgmodels.EmailAddress(email),
	}
	contact.Subscribed = true
	// ACQ-007: capture consent provenance — the contact opted in by submitting
	// this form. Recorded before the plan-hold masks Subscribed so consent
	// survives a limit-hold/release (BILL-010).
	consent := true
	contact.ConsentSubscribed = &consent
	contact.ConsentSource = "form:" + form.PublicId
	contact.ConsentedAt = &now
	contact.SoftDeletes.CreatedAt = &now
	plans.ApplyHold(&contact)
	if err := col.Insert(contact); err != nil {
		return nil, err
	}
	plans.Invalidate(form.TenantID)
	return &contact, nil
}

// emailFromSubmission resolves the contact's email by inspecting the form's
// FormField.MapsTo declarations. Falls back to a top-level "email" key for
// public callers that submit raw key/value pairs.
func emailFromSubmission(form *pkgmodels.PageForm, sub Submission) string {
	for _, f := range form.Fields {
		if f == nil {
			continue
		}
		if strings.EqualFold(f.MapsTo, "email") || strings.EqualFold(f.FieldType, "email") {
			if v, ok := sub.FieldValues[f.FieldName]; ok && v != "" {
				return v
			}
		}
	}
	return sub.FieldValues["email"]
}

// writeAttributes maps each FormField with a non-empty MapsTo into the user
// record. Top-level columns (email, first_name, last_name, phone_number) get
// their dedicated fields; everything else lands in CustomFields, coerced to
// the FieldType the form declared (number→float64, boolean→bool, date→time).
func writeAttributes(form *pkgmodels.PageForm, sub Submission, contact *pkgmodels.User) {
	if contact == nil {
		return
	}
	set := bson.M{}
	custom := map[string]any{}
	for _, f := range form.Fields {
		if f == nil || f.MapsTo == "" {
			continue
		}
		val, ok := sub.FieldValues[f.FieldName]
		if !ok || val == "" {
			continue
		}
		switch strings.ToLower(f.MapsTo) {
		case "email":
			set["email"] = pkgmodels.EmailAddress(strings.ToLower(strings.TrimSpace(val)))
		case "first_name", "name.first_name":
			set["name.first_name"] = val
		case "last_name", "name.last_name":
			set["name.last_name"] = val
		case "phone", "phone_number":
			set["phone_number"] = val
		default:
			custom[f.MapsTo] = coerceFieldValue(f.FieldType, val)
		}
	}
	if len(custom) > 0 {
		for k, v := range custom {
			set["custom_fields."+k] = v
		}
	}
	if len(set) == 0 {
		return
	}
	now := time.Now()
	set["timestamps.updated_at"] = now
	if err := db.GetCollection(pkgmodels.UserCollection).Update(
		bson.M{"_id": contact.Id},
		bson.M{"$set": set},
	); err != nil {
		log.Printf("forms.executor: writeAttributes failed: %v", err)
	}
}

// coerceFieldValue converts a wire-format string submission to the typed
// value the FormField declares. Unknown or unparseable inputs fall back to
// the raw string so we never silently drop data.
func coerceFieldValue(fieldType, raw string) any {
	switch strings.ToLower(strings.TrimSpace(fieldType)) {
	case "number":
		if n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
			return n
		}
	case "boolean", "bool":
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off", "":
			return false
		}
	case "date", "datetime":
		s := strings.TrimSpace(raw)
		// Accept full ISO 8601 and the bare "yyyy-mm-dd" the HTML date input
		// produces.
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t
			}
		}
	case "multiselect":
		// Wire format: comma-separated selections.
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if v := strings.TrimSpace(p); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	return raw
}

// validateOptions drops submitted values that aren't among a choice field's
// declared options (select/radio: whole value; multiselect: per selection).
// Returns warnings for anything dropped — consistent with the executor's
// non-fatal posture.
func validateOptions(form *pkgmodels.PageForm, values map[string]string) []string {
	var warnings []string
	allowed := func(f *pkgmodels.FormField, v string) bool {
		for _, o := range f.Options {
			if o == v {
				return true
			}
		}
		return false
	}
	for _, f := range form.Fields {
		if f == nil || len(f.Options) == 0 {
			continue
		}
		raw, present := values[f.FieldName]
		if !present || raw == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(f.FieldType)) {
		case "select", "radio":
			if !allowed(f, raw) {
				warnings = append(warnings, "invalid_option: "+f.FieldName+"="+raw)
				delete(values, f.FieldName)
			}
		case "multiselect":
			parts := strings.Split(raw, ",")
			kept := make([]string, 0, len(parts))
			for _, p := range parts {
				v := strings.TrimSpace(p)
				if v == "" {
					continue
				}
				if allowed(f, v) {
					kept = append(kept, v)
				} else {
					warnings = append(warnings, "invalid_option: "+f.FieldName+"="+v)
				}
			}
			if len(kept) == 0 {
				delete(values, f.FieldName)
			} else {
				values[f.FieldName] = strings.Join(kept, ",")
			}
		}
	}
	return warnings
}

// recordSubmission persists the raw (option-validated, type-coerced) answers
// as a FormSubmission row so tenants can review per-form responses.
func recordSubmission(form *pkgmodels.PageForm, sub Submission, contact *pkgmodels.User) {
	data := map[string]interface{}{}
	typed := map[string]string{}
	for _, f := range form.Fields {
		if f != nil && f.FieldName != "" {
			typed[f.FieldName] = f.FieldType
		}
	}
	for k, v := range sub.FieldValues {
		data[k] = coerceFieldValue(typed[k], v)
	}
	row := pkgmodels.FormSubmission{
		Id:        bson.NewObjectId(),
		PublicId:  utils.GeneratePublicId(),
		TenantID:  form.TenantID,
		FormID:    form.PublicId,
		Data:      data,
		Source:    sub.Source,
		CreatedAt: time.Now(),
	}
	if contact != nil {
		row.ContactID = contact.PublicId
		row.ContactEmail = string(contact.Email)
	}
	if err := db.GetCollection(pkgmodels.FormSubmissionCollection).Insert(row); err != nil {
		log.Printf("forms: failed to record submission for form %s: %v", form.PublicId, err)
	}
}

// scopedFind builds a (public_id + tenant_or_subscriber) filter. Different
// generations of the schema scoped collections by tenant_id (Badge, Story)
// or subscriber_id (EmailList), so we $or-match either to be safe — same
// pattern as serve_site_resources.go's tenantScope helper.
func scopedFind(tenantID bson.ObjectId, publicID string) bson.M {
	return bson.M{
		"public_id": publicID,
		"$or": []bson.M{
			{"tenant_id": tenantID},
			{"subscriber_id": tenantID.Hex()},
		},
	}
}

// ── badges ────────────────────────────────────────────────────────────────

func assignBadge(tenantID, contactID bson.ObjectId, badgePublicID string) (string, bool) {
	var badge pkgmodels.Badge
	if err := db.GetCollection(pkgmodels.BadgeCollection).Find(scopedFind(tenantID, badgePublicID)).One(&badge); err != nil {
		return "", false
	}
	// ID-012: mutation through the badge command records provenance.
	if _, err := badges.Assign(tenantID, contactID, badge.Id, "form_action", "", "system"); err != nil {
		log.Printf("forms.executor: assignBadge failed: %v", err)
		return "", false
	}
	return badge.Name, true
}

func removeBadge(tenantID, contactID bson.ObjectId, badgePublicID string) (string, bool) {
	var badge pkgmodels.Badge
	if err := db.GetCollection(pkgmodels.BadgeCollection).Find(scopedFind(tenantID, badgePublicID)).One(&badge); err != nil {
		return "", false
	}
	if err := badges.Remove(tenantID, contactID, badge.Id, "form_action", "", "system"); err != nil {
		log.Printf("forms.executor: removeBadge failed: %v", err)
		return "", false
	}
	return badge.Name, true
}

// ── email lists ───────────────────────────────────────────────────────────

func addToList(tenantID, contactID bson.ObjectId, listPublicID string) (string, bool) {
	var list pkgmodels.EmailList
	if err := db.GetCollection(pkgmodels.EmailListCollection).Find(scopedFind(tenantID, listPublicID)).One(&list); err != nil {
		return "", false
	}
	if err := db.GetCollection(pkgmodels.EmailListCollection).Update(
		bson.M{"_id": list.Id},
		bson.M{
			"$addToSet": bson.M{"active": contactID},
			"$pull":     bson.M{"removed": contactID},
		},
	); err != nil {
		log.Printf("forms.executor: addToList failed: %v", err)
		return "", false
	}
	return list.Name, true
}

func removeFromList(tenantID, contactID bson.ObjectId, listPublicID string) (string, bool) {
	var list pkgmodels.EmailList
	if err := db.GetCollection(pkgmodels.EmailListCollection).Find(scopedFind(tenantID, listPublicID)).One(&list); err != nil {
		return "", false
	}
	if err := db.GetCollection(pkgmodels.EmailListCollection).Update(
		bson.M{"_id": list.Id},
		bson.M{
			"$addToSet": bson.M{"removed": contactID},
			"$pull":     bson.M{"active": contactID},
		},
	); err != nil {
		log.Printf("forms.executor: removeFromList failed: %v", err)
		return "", false
	}
	return list.Name, true
}

// ── stories ───────────────────────────────────────────────────────────────

// startStory resolves a Story by public_id within the tenant's audience and
// dispatches the existing TriggerStoryStart helper (which calls
// core-service /internal/story/start). Story names can collide and rename, so
// we store IDs and resolve to name at execution time.
func startStory(tenantID bson.ObjectId, contactPublicID, storyPublicID string) (string, bool) {
	q := scopedFind(tenantID, storyPublicID)
	q["timestamps.deleted_at"] = nil
	var story pkgmodels.Story
	if err := db.GetCollection(pkgmodels.StoryCollection).Find(q).One(&story); err != nil {
		return "", false
	}
	// ACQ-003: durable story-start command (retried, dead-lettered) instead
	// of a fire-and-forget goroutine.
	if err := routes.EnqueueStoryStart(tenantID.Hex(), story.Name, contactPublicID); err != nil {
		log.Printf("forms.executor: enqueue story start failed: %v", err)
		return "", false
	}
	return story.Name, true
}

// ── product grants (pre-Stripe, free grant for lead-magnet flows) ─────────

func grantProductAccess(tenantID bson.ObjectId, contact *pkgmodels.User, productPublicID, videoSessionID string) bool {
	var product pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"public_id":             productPublicID,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&product); err != nil {
		return false
	}
	now := time.Now()
	logEntry := pkgmodels.PurchaseLog{
		Id:                   bson.NewObjectId(),
		PublicId:             utils.GeneratePublicId(),
		TenantID:             tenantID,
		SubscriberId:         contact.SubscriberId,
		UserId:               contact.Id,
		ProductId:            product.Id,
		Status:               "granted_via_form",
		Amount:               0,
		Currency:             "usd",
		VideoSessionPublicId: videoSessionID,
	}
	logEntry.SoftDeletes.CreatedAt = &now
	if err := db.GetCollection(pkgmodels.PurchaseLogCollection).Insert(logEntry); err != nil {
		log.Printf("forms.executor: grantProductAccess insert failed: %v", err)
		return false
	}
	// ACQ-005: the durable entitlement is an explicit AccessGrant (the same
	// authority the library/authorization layer consults — survives the
	// ACCESS_GRANTS_ONLY flip), not just the attribution PurchaseLog above.
	// Idempotent per (tenant, contact, product, source).
	grants := db.GetCollection(pkgmodels.AccessGrantCollection)
	n, _ := grants.Find(bson.M{
		"tenant_id":  tenantID,
		"contact_id": contact.Id,
		"product_id": product.Id,
		"source":     "form_grant",
		"status":     pkgmodels.GrantStatusActive,
	}).Count()
	if n == 0 {
		grant := pkgmodels.NewAccessGrant(tenantID, contact.Id, product.Id, "", "", "form_grant")
		if err := grants.Insert(grant); err != nil {
			log.Printf("forms.executor: grantProductAccess grant insert failed: %v", err)
		}
	}
	return true
}

// ── lead-magnet email delivery ────────────────────────────────────────────

// sendDownloadDeliveryEmail mails the signed download URLs to the contact.
// Mirrors the SMTP wiring used by the Stripe webhook's password-setup mail
// (mailhog in dev, real SMTP in prod). Reuses tenant.BusinessName + tenant
// MailgunDomain only as the From address; the body is a plain bullet list of
// download names linking to the signed URLs.
func sendDownloadDeliveryEmail(tenantID bson.ObjectId, contact *pkgmodels.User, downloads []routes.SignedDownload) error {
	if contact == nil || contact.Email == "" || len(downloads) == 0 {
		return nil
	}
	provider, from := selectEmailProvider(tenantID)
	if provider == nil {
		// In dev without mailhog this is expected — log + continue rather
		// than fail the action chain.
		log.Printf("forms.executor: no email provider; skipping download delivery to %s", contact.Email)
		return nil
	}

	first := contact.Name.First
	if first == "" {
		first = "there"
	}

	var body strings.Builder
	body.WriteString("<p>Hi ")
	body.WriteString(htmlpkg.EscapeString(first))
	body.WriteString(",</p>")
	body.WriteString("<p>Thanks for opting in. Your download")
	if len(downloads) > 1 {
		body.WriteString("s are")
	} else {
		body.WriteString(" is")
	}
	body.WriteString(" ready:</p><ul>")
	for _, d := range downloads {
		name := d.Name
		if name == "" {
			name = "Download"
		}
		body.WriteString(fmt.Sprintf(`<li><a href="%s">%s</a></li>`,
			htmlpkg.EscapeString(d.URL), htmlpkg.EscapeString(name)))
	}
	body.WriteString("</ul>")
	body.WriteString(fmt.Sprintf("<p style=\"color:#6b7280;font-size:12px\">These links expire in %s.</p>",
		humanizeDuration(downloadEmailTTL)))

	subject := "Your download is ready"
	if len(downloads) > 1 {
		subject = "Your downloads are ready"
	}
	return provider.SendEmail(from, string(contact.Email), subject, body.String(), "")
}

// selectEmailProvider returns a tenant-aware SMTP provider + From address.
// Mirrors the helper in serve_stripe_webhook.go (kept duplicated rather than
// exported to avoid a marketing-service/handlers → internal/forms cycle).
func selectEmailProvider(tenantID bson.ObjectId) (email.EmailProvider, string) {
	from := "no-reply@sentanyl.local"
	var tenant pkgmodels.Tenant
	if err := db.GetCollection(pkgmodels.TenantCollection).FindId(tenantID).One(&tenant); err == nil {
		if tenant.MailgunDomain != "" {
			from = "no-reply@" + tenant.MailgunDomain
		}
	}
	return email.DefaultProvider(), from
}

func humanizeDuration(d time.Duration) string {
	hours := int(d.Hours())
	if hours >= 24 && hours%24 == 0 {
		days := hours / 24
		if days == 1 {
			return "24 hours"
		}
		return fmt.Sprintf("%d days", days)
	}
	if hours == 1 {
		return "1 hour"
	}
	if hours > 1 {
		return fmt.Sprintf("%d hours", hours)
	}
	return d.String()
}

// silence unused import when only some helpers are exercised by tests.
var _ = smtp.SendMail
