package routes

import (
	"fmt"
	"log"
	"strings"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
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

	q := bson.M{"tenant_id": tenantID}
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
		tracked := fmt.Sprintf("/api/marketing/campaigns/track/click?c=%s&r={{REC_PUBLIC_ID}}&u=%s",
			campaignPubID, urlQueryEscape(href))
		if badge != "" {
			tracked += "&b=" + urlQueryEscape(badge)
		}
		return fmt.Sprintf(`<a %shref="%s"%s>`, pre, tracked, post)
	})
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

// dispatchCampaign resolves the campaign's audience, expands per-recipient
// emails (with rewritten click links), inserts CampaignRecipient + Email rows,
// and sends instant rows immediately via SMTP. Scheduled rows ride
// ScheduledEmailCollection (existing in-process scheduler).
//
// Caller is responsible for setting Campaign.Status before/after. This function
// only writes recipient rows and email rows; it leaves status untouched.
func dispatchCampaign(camp *pkgmodels.Campaign, scheduled bool, scheduledAt time.Time) (int, error) {
	if camp == nil {
		return 0, fmt.Errorf("nil campaign")
	}
	users, err := resolveCampaignAudience(camp.TenantID, camp.Audience)
	if err != nil {
		return 0, err
	}
	if len(users) == 0 {
		return 0, nil
	}

	bodyHTML := rewriteCampaignLinks(camp.Body, camp.PublicId, camp.ClickRules)

	from := camp.FromEmail
	if from == "" {
		from = "no-reply@sentanyl.local"
	}

	count := 0
	for _, u := range users {
		recipient := pkgmodels.NewCampaignRecipient(camp.Id, camp.TenantID, u.Id, string(u.Email))
		if err := db.GetCollection(pkgmodels.CampaignRecipientCollection).Insert(recipient); err != nil {
			log.Printf("campaign: recipient insert failed: %v", err)
			continue
		}

		var msg *pkgmodels.Email
		if scheduled {
			msg = pkgmodels.NewScheduledEmail()
			msg.Scheduled = &scheduledAt
		} else {
			msg = pkgmodels.NewInstantEmail()
		}
		msg.From = from
		msg.To = string(u.Email)
		msg.SubjectLine = camp.Subject
		msg.Html = strings.ReplaceAll(bodyHTML, "{{REC_PUBLIC_ID}}", recipient.PublicId)
		msg.ReplyTo = camp.ReplyTo

		col := pkgmodels.InstantEmailCollection
		if scheduled {
			col = pkgmodels.ScheduledEmailCollection
		}
		if err := db.GetCollection(col).Insert(msg); err != nil {
			log.Printf("campaign: email insert failed: %v", err)
			continue
		}

		if !scheduled && smtpProvider != nil {
			if err := smtpProvider.SendEmail(msg.From, msg.To, msg.SubjectLine, msg.Html, msg.ReplyTo); err != nil {
				log.Printf("campaign: SMTP send failed for %s: %v", msg.To, err)
				_ = db.GetCollection(pkgmodels.CampaignRecipientCollection).Update(
					bson.M{"_id": recipient.Id},
					bson.M{"$set": bson.M{"status": "failed", "last_error": err.Error()}},
				)
				continue
			}
			now := time.Now()
			_ = db.GetCollection(pkgmodels.CampaignRecipientCollection).Update(
				bson.M{"_id": recipient.Id},
				bson.M{"$set": bson.M{"status": "sent", "sent_at": now}},
			)
		}
		count++
	}

	return count, nil
}
