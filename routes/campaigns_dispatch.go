package routes

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/jobs"

	"github.com/josephalai/sentanyl/pkg/sendauth"
	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/emailer"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// resolveCampaignAudience translates a campaign's badge constraints (by public_id
// or name) into a list of users matching the must-have / must-not-have rules.
// Empty lists mean "no constraint on that side".
func resolveCampaignAudience(tenantID bson.ObjectId, aud pkgmodels.CampaignAudience) ([]pkgmodels.User, error) {
	mustIDs, err := lookupBadgeObjectIDs(tenantID, aud.MustHave)
	if err != nil {
		return nil, fmt.Errorf("must_have lookup: %w", err)
	}
	mustNotIDs, err := lookupBadgeObjectIDs(tenantID, aud.MustNotHave)
	if err != nil {
		return nil, fmt.Errorf("must_not_have lookup: %w", err)
	}

	// Contacts who used the one-click unsubscribe are suppressed from every
	// bulk channel that resolves audiences here (campaigns + A/B).
	q := bson.M{"tenant_id": tenantID, "unsubscribed_at": nil}
	if len(mustIDs) > 0 {
		q["badges"] = bson.M{"$all": mustIDs}
	}
	if len(mustNotIDs) > 0 {
		// Combine with $all if present, else just $nin.
		if existing, ok := q["badges"].(bson.M); ok {
			existing["$nin"] = mustNotIDs
			q["badges"] = existing
		} else {
			q["badges"] = bson.M{"$nin": mustNotIDs}
		}
	}

	var users []pkgmodels.User
	if err := db.GetCollection(pkgmodels.UserCollection).Find(q).All(&users); err != nil {
		return nil, fmt.Errorf("user query: %w", err)
	}
	return users, nil
}

// lookupBadgeObjectIDs accepts a slice of badge identifiers (public_id or name)
// and returns their ObjectIds. Unknown identifiers are silently skipped — a
// non-existent badge cannot match any user, so the campaign just narrows.
func lookupBadgeObjectIDs(tenantID bson.ObjectId, idents []string) ([]bson.ObjectId, error) {
	if len(idents) == 0 {
		return nil, nil
	}
	q := bson.M{
		"tenant_id": tenantID,
		"$or": []bson.M{
			{"public_id": bson.M{"$in": idents}},
			{"name": bson.M{"$in": idents}},
		},
	}
	var badges []pkgmodels.Badge
	if err := db.GetCollection(pkgmodels.BadgeCollection).Find(q).All(&badges); err != nil {
		return nil, err
	}
	out := make([]bson.ObjectId, 0, len(badges))
	for _, b := range badges {
		out = append(out, b.Id)
	}
	return out, nil
}

// rewriteCampaignLinks wraps every <a href="..."> in the body so it points at
// the per-recipient click tracker. Excludes mailto:, tel:, and #fragment links.
// {{REC_PUBLIC_ID}} is filled in at send time per recipient.
//
// If the href matches one of the campaign's click rules (URL prefix match), the
// tracker URL also carries `b=<badgePublicId>` so the tracker awards that badge
// on click.
func rewriteCampaignLinks(body, campaignPubID string, rules []pkgmodels.CampaignClickRule) string {
	return linkHrefRE.ReplaceAllStringFunc(body, func(match string) string {
		groups := linkHrefRE.FindStringSubmatch(match)
		if len(groups) < 4 {
			return match
		}
		pre, href, post := groups[1], groups[2], groups[3]
		hl := strings.ToLower(href)
		if strings.HasPrefix(hl, "mailto:") || strings.HasPrefix(hl, "tel:") || strings.HasPrefix(hl, "#") || strings.Contains(href, "/track/") || strings.Contains(href, "{{") {
			return match
		}

		badge := matchClickBadge(href, rules)
		// COM-EM-006: absolute destinations ride a signed token minted per
		// recipient (the {{CLICK_TOKEN|…}} placeholder resolves at send
		// time), so the tracker never sees a raw external redirect target.
		// Relative destinations keep the legacy u= form, which the tracker
		// only honors same-origin.
		var tracked string
		if strings.HasPrefix(hl, "http://") || strings.HasPrefix(hl, "https://") {
			tracked = fmt.Sprintf("%s/api/marketing/campaigns/track/click?c=%s&r={{REC_PUBLIC_ID}}&e={{SEND_PUBLIC_ID}}&t=%s",
				publicBaseURL(), campaignPubID, clickTokenPlaceholder(href))
		} else {
			tracked = fmt.Sprintf("%s/api/marketing/campaigns/track/click?c=%s&r={{REC_PUBLIC_ID}}&e={{SEND_PUBLIC_ID}}&u=%s",
				publicBaseURL(), campaignPubID, urlQueryEscape(href))
		}
		if badge != "" {
			tracked += "&b=" + urlQueryEscape(badge)
		}
		return fmt.Sprintf(`<a %shref="%s"%s>`, pre, tracked, post)
	})
}

// campaignOpenPixel appends the unified 1x1 open pixel. The pixel carries a
// signed open token minted per recipient (COM-EM-006) instead of a bare
// send id, so opens cannot be stamped by enumeration.
func campaignOpenPixel(body string) string {
	pixel := `<img src="` + publicBaseURL() + `/api/marketing/track/open?t={{OPEN_TOKEN}}" width="1" height="1" style="display:none" alt=""/>`
	if i := strings.LastIndex(body, "</body>"); i >= 0 {
		return body[:i] + pixel + body[i:]
	}
	return body + pixel
}

// matchClickBadge returns the badge identifier for the first click rule whose
// URL pattern matches the given href, or "" if none match.
func matchClickBadge(href string, rules []pkgmodels.CampaignClickRule) string {
	for _, r := range rules {
		if r.AwardBadge == "" {
			continue
		}
		if r.URLPattern == "" {
			// Unconditional: every link awards this badge.
			return r.AwardBadge
		}
		if strings.HasPrefix(href, r.URLPattern) || href == r.URLPattern {
			return r.AwardBadge
		}
	}
	return ""
}

// EnsureCampaignIndexes creates the durable-dispatch invariant (COM-EM-005):
// one recipient row per (campaign, user), so a resumed dispatch can never
// double-expand the audience.
func EnsureCampaignIndexes() {
	if err := db.GetCollection(pkgmodels.CampaignRecipientCollection).EnsureIndex(mgo.Index{
		Key:        []string{"campaign_id", "user_id"},
		Unique:     true,
		Background: true,
	}); err != nil {
		log.Printf("campaigns: recipient index: %v", err)
	}
}

const campaignDispatchJobType = "campaign.dispatch"

// RegisterCampaignDispatchJob wires the durable campaign dispatcher
// (COM-EM-005). A send request only enqueues; this handler expands the
// audience into CampaignRecipient rows (idempotent via the unique index) and
// delivers every still-pending row. It is fully re-entrant: a crashed or
// killed worker resumes on the retry without duplicate sends — each
// recipient is claimed pending→sending with a CAS before its single send
// attempt (at-most-once per recipient by design: losing one send on a crash
// beats double-mailing a list).
func RegisterCampaignDispatchJob() {
	jobs.Register(campaignDispatchJobType, func(ctx context.Context, job *jobs.Job) error {
		idHex, _ := job.Payload["campaign_id"].(string)
		if !bson.IsObjectIdHex(idHex) {
			return nil
		}
		var camp pkgmodels.Campaign
		if err := db.GetCollection(pkgmodels.CampaignCollection).FindId(bson.ObjectIdHex(idHex)).One(&camp); err != nil {
			return nil // campaign gone — nothing to do
		}
		if camp.Status == pkgmodels.CampaignStatusCanceled {
			return nil
		}
		if err := expandCampaignAudience(&camp); err != nil {
			return err // retryable
		}
		sent, failed, err := deliverCampaignPending(&camp)
		if err != nil {
			return err // retryable — pending rows resume next attempt
		}
		now := time.Now()
		_ = db.GetCollection(pkgmodels.CampaignCollection).Update(
			bson.M{"_id": camp.Id, "status": pkgmodels.CampaignStatusSending},
			bson.M{"$set": bson.M{
				"status":          pkgmodels.CampaignStatusSent,
				"sent_at":         now,
				"recipient_count": sent,
			}},
		)
		if failed > 0 {
			log.Printf("campaign %s: %d recipients failed permanently (see recipient rows)", camp.PublicId, failed)
		}
		return nil
	})
}

// EnqueueCampaignDispatch schedules the durable dispatch for a campaign.
// Idempotent per campaign: a duplicate send request cannot double-dispatch.
func EnqueueCampaignDispatch(camp *pkgmodels.Campaign) error {
	return jobs.Enqueue(jobs.NewJob(campaignDispatchJobType,
		campaignDispatchJobType+":"+camp.Id.Hex(),
		jobs.Envelope{TenantID: camp.TenantID, Actor: "campaigns", Subject: camp.PublicId, Version: 1},
		bson.M{"campaign_id": camp.Id.Hex()},
	))
}

// expandCampaignAudience upserts one pending CampaignRecipient per audience
// member. Existing rows (from an earlier attempt) are left untouched.
func expandCampaignAudience(camp *pkgmodels.Campaign) error {
	users, err := resolveCampaignAudience(camp.TenantID, camp.Audience)
	if err != nil {
		return err
	}
	col := db.GetCollection(pkgmodels.CampaignRecipientCollection)
	for _, u := range users {
		recipient := pkgmodels.NewCampaignRecipient(camp.Id, camp.TenantID, u.Id, string(u.Email))
		if err := col.Insert(recipient); err != nil && !mgo.IsDup(err) {
			return err
		}
	}
	return nil
}

// deliverCampaignPending renders the campaign once and delivers every
// pending recipient row, claiming each with a CAS so concurrent or resumed
// passes never double-send. Returns totals for the whole campaign.
func deliverCampaignPending(camp *pkgmodels.Campaign) (sentTotal, failedTotal int, err error) {
	bodyHTML := campaignOpenPixel(rewriteCampaignLinks(camp.Body, camp.PublicId, camp.ClickRules))
	from := camp.FromEmail
	if from == "" {
		from = "no-reply@sentanyl.local"
	}
	postal := emailer.TenantPostalAddress(camp.TenantID.Hex())
	col := db.GetCollection(pkgmodels.CampaignRecipientCollection)

	var pending []pkgmodels.CampaignRecipient
	if err := col.Find(bson.M{"campaign_id": camp.Id, "status": "pending"}).All(&pending); err != nil {
		return 0, 0, err
	}
	for _, recipient := range pending {
		// Cancellation is honored between recipients.
		var cur pkgmodels.Campaign
		if err := db.GetCollection(pkgmodels.CampaignCollection).FindId(camp.Id).Select(bson.M{"status": 1}).One(&cur); err == nil &&
			cur.Status == pkgmodels.CampaignStatusCanceled {
			break
		}
		// CAS claim: only the pass that flips pending→sending delivers.
		if e := col.Update(bson.M{"_id": recipient.Id, "status": "pending"},
			bson.M{"$set": bson.M{"status": "sending"}}); e != nil {
			continue // claimed elsewhere / already handled
		}
		var u pkgmodels.User
		if e := db.GetCollection(pkgmodels.UserCollection).FindId(recipient.UserID).One(&u); e != nil {
			_ = col.Update(bson.M{"_id": recipient.Id}, bson.M{"$set": bson.M{"status": "failed", "last_error": "user missing"}})
			failedTotal++
			continue
		}
		if e := sendCampaignRecipient(camp, &recipient, &u, bodyHTML, from, postal); e != nil {
			_ = col.Update(bson.M{"_id": recipient.Id}, bson.M{"$set": bson.M{"status": "failed", "last_error": e.Error()}})
			failedTotal++
			continue
		}
		now := time.Now()
		_ = col.Update(bson.M{"_id": recipient.Id}, bson.M{"$set": bson.M{"status": "sent", "sent_at": now}})
	}
	// Totals reflect ALL rows (this pass + earlier resumed passes).
	sentTotal, _ = col.Find(bson.M{"campaign_id": camp.Id, "status": "sent"}).Count()
	failedTotal, _ = col.Find(bson.M{"campaign_id": camp.Id, "status": "failed"}).Count()
	return sentTotal, failedTotal, nil
}

// sendCampaignRecipient runs the per-recipient pipeline: send authority,
// unified EmailSend row, personalized signed rendering, provider send.
func sendCampaignRecipient(camp *pkgmodels.Campaign, recipient *pkgmodels.CampaignRecipient, u *pkgmodels.User, bodyHTML, from, postal string) error {
	unsubURL := emailer.UnsubURL(publicBaseURL(), u.PublicId)
	dec := sendauth.Authorize(sendauth.Request{
		TenantID: camp.TenantID,
		Email:    string(u.Email),
		Class:    sendauth.Marketing,
		Purpose:  "campaign",
		UnsubURL: unsubURL,
	})
	if !dec.Allowed {
		return fmt.Errorf("send authority refused: %s", dec.Reason)
	}

	send := pkgmodels.NewEmailSend(camp.TenantID, pkgmodels.EmailSendSourceCampaign, string(u.Email), camp.Subject)
	send.ContactPublicID = u.PublicId
	send.CampaignPublicID = camp.PublicId
	msgID, verpReplyTo := emailer.ReplyCorrelation(send.PublicId)
	send.MessageID = msgID
	if err := db.GetCollection(pkgmodels.EmailSendCollection).Insert(send); err != nil {
		log.Printf("campaign: email send row insert failed: %v", err)
	}

	msg := pkgmodels.NewInstantEmail()
	msg.From = from
	msg.To = string(u.Email)
	msg.SubjectLine = camp.Subject
	msg.Html = strings.ReplaceAll(bodyHTML, "{{REC_PUBLIC_ID}}", recipient.PublicId)
	msg.Html = strings.ReplaceAll(msg.Html, "{{SEND_PUBLIC_ID}}", send.PublicId)
	msg.Html = signEmailTrackingPlaceholders(msg.Html, camp.TenantID.Hex(), send.PublicId)
	msg.Html = emailer.AppendUnsubFooter(msg.Html, unsubURL, postal)
	msg.ReplyTo = camp.ReplyTo
	if msg.ReplyTo == "" {
		msg.ReplyTo = verpReplyTo
	}
	if err := db.GetCollection(pkgmodels.InstantEmailCollection).Insert(msg); err != nil {
		return err
	}

	if smtpProvider == nil {
		return nil // dev stacks without a provider still record the send
	}
	if hs, ok := smtpProvider.(emailer.HeaderSender); ok {
		headers := dec.Headers
		if headers == nil {
			headers = map[string]string{}
		}
		if msgID != "" {
			headers["Message-ID"] = msgID
		}
		return hs.SendEmailWithHeaders(msg.From, msg.To, msg.SubjectLine, msg.Html, msg.ReplyTo, headers)
	}
	return smtpProvider.SendEmail(msg.From, msg.To, msg.SubjectLine, msg.Html, msg.ReplyTo)
}

// dispatchCampaignScheduled pre-expands the audience and writes scheduled
// email rows for the legacy scheduled path (rows ride
// ScheduledEmailCollection until their send time).
func dispatchCampaignScheduled(camp *pkgmodels.Campaign, scheduledAt time.Time) (int, error) {
	if camp == nil {
		return 0, fmt.Errorf("nil campaign")
	}
	users, err := resolveCampaignAudience(camp.TenantID, camp.Audience)
	if err != nil {
		return 0, err
	}
	bodyHTML := campaignOpenPixel(rewriteCampaignLinks(camp.Body, camp.PublicId, camp.ClickRules))
	from := camp.FromEmail
	if from == "" {
		from = "no-reply@sentanyl.local"
	}
	postal := emailer.TenantPostalAddress(camp.TenantID.Hex())
	count := 0
	for _, u := range users {
		unsubURL := emailer.UnsubURL(publicBaseURL(), u.PublicId)
		dec := sendauth.Authorize(sendauth.Request{
			TenantID: camp.TenantID,
			Email:    string(u.Email),
			Class:    sendauth.Marketing,
			Purpose:  "campaign",
			UnsubURL: unsubURL,
		})
		if !dec.Allowed {
			log.Printf("campaign: skip recipient %s (reason=%s)", u.Email, dec.Reason)
			continue
		}
		recipient := pkgmodels.NewCampaignRecipient(camp.Id, camp.TenantID, u.Id, string(u.Email))
		recipient.Status = "scheduled"
		if err := db.GetCollection(pkgmodels.CampaignRecipientCollection).Insert(recipient); err != nil {
			if !mgo.IsDup(err) {
				log.Printf("campaign: recipient insert failed: %v", err)
			}
			continue
		}
		send := pkgmodels.NewEmailSend(camp.TenantID, pkgmodels.EmailSendSourceCampaign, string(u.Email), camp.Subject)
		send.ContactPublicID = u.PublicId
		send.CampaignPublicID = camp.PublicId
		msgID, verpReplyTo := emailer.ReplyCorrelation(send.PublicId)
		send.MessageID = msgID
		_ = db.GetCollection(pkgmodels.EmailSendCollection).Insert(send)
		_ = msgID

		msg := pkgmodels.NewScheduledEmail()
		msg.Scheduled = &scheduledAt
		msg.From = from
		msg.To = string(u.Email)
		msg.SubjectLine = camp.Subject
		msg.Html = strings.ReplaceAll(bodyHTML, "{{REC_PUBLIC_ID}}", recipient.PublicId)
		msg.Html = strings.ReplaceAll(msg.Html, "{{SEND_PUBLIC_ID}}", send.PublicId)
		msg.Html = signEmailTrackingPlaceholders(msg.Html, camp.TenantID.Hex(), send.PublicId)
		msg.Html = emailer.AppendUnsubFooter(msg.Html, unsubURL, postal)
		msg.ReplyTo = camp.ReplyTo
		if msg.ReplyTo == "" {
			msg.ReplyTo = verpReplyTo
		}
		if err := db.GetCollection(pkgmodels.ScheduledEmailCollection).Insert(msg); err != nil {
			log.Printf("campaign: scheduled email insert failed: %v", err)
			continue
		}
		count++
	}
	return count, nil
}
