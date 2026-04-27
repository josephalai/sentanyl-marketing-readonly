package routes

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/ai"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/render"
)

// broadcastNewsletterPost fans a published post out to its target audience by
// inserting one Email document per recipient. Instant rows go through the
// existing scheduler/SMTPProvider path that funnels and password-setup emails
// already use; scheduled rows ride ScheduledEmailCollection (the in-process
// scheduler picks them up at the configured Scheduled time).
//
// The subject line falls back to the post title when not configured. The HTML
// body is the rendered post sliced at the subscriber-break (anonymous-safe
// content goes in the email; the "read full post" CTA points at the public
// post URL).
//
// Returns the number of email rows successfully inserted.
func broadcastNewsletterPost(p *pkgmodels.Product, post *pkgmodels.NewsletterPost, scheduled bool, scheduledAt time.Time) (int, error) {
	cfg := p.Newsletter
	if cfg == nil {
		cfg = &pkgmodels.NewsletterConfig{}
	}

	// Audience filter on the subscription collection. Status must be active.
	q := bson.M{
		"tenant_id":  p.TenantID,
		"product_id": p.Id,
		"status":     pkgmodels.NewsletterSubscriptionStatusActive,
	}
	switch post.Audience {
	case pkgmodels.NewsletterAudienceNone:
		return 0, nil
	case pkgmodels.NewsletterAudiencePaidOnly:
		q["tier_id"] = bson.M{"$ne": pkgmodels.NewsletterFreeTierID}
	case pkgmodels.NewsletterAudienceFreeOnly:
		q["tier_id"] = pkgmodels.NewsletterFreeTierID
	case pkgmodels.NewsletterAudienceTierIDs:
		if len(post.AudienceTierIDs) > 0 {
			ids := make([]string, 0, len(post.AudienceTierIDs))
			for _, id := range post.AudienceTierIDs {
				ids = append(ids, id.Hex())
			}
			q["tier_id"] = bson.M{"$in": ids}
		}
	}

	var subs []pkgmodels.NewsletterSubscription
	if err := db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Find(q).All(&subs); err != nil {
		return 0, fmt.Errorf("audience query: %w", err)
	}

	subject := post.EmailSubject
	if subject == "" {
		subject = post.Title
	}
	if subject == "" {
		subject = "New post from " + p.Name
	}

	from := cfg.FromEmail
	if from == "" {
		from = "no-reply@sentanyl.local"
	}

	// Resolve {{ai}} handlebars in subject + preview + body BEFORE the
	// gate split + per-recipient send loop. The resolver caches per
	// (tenant, prompt, time-bucket), so the first hit talks to the LLM
	// and every subsequent recipient in this broadcast gets the same
	// value — exactly the user's requirement: "the content of the
	// Newsletter changes every {timeframe} but is sent to everyone
	// within that timeframe."
	resolvedHTML := post.RenderedHTML
	if resolver := ai.Resolver(); resolver != nil {
		opts := render.ResolveOptions{
			TenantID:             p.TenantID,
			PostContextPackIDs:   post.ContextPackIDs,
			NewsletterDefaults:   cfg.DefaultContextPackIDs,
			NewsletterTTLSeconds: cfg.DefaultAITTLSeconds,
		}
		subject = resolver.Resolve(subject, opts)
		post.EmailPreviewText = resolver.Resolve(post.EmailPreviewText, opts)
		resolvedHTML = resolver.Resolve(post.RenderedHTML, opts)
	}

	// Slice rendered HTML at the subscriber-break so the email body is the
	// public-safe portion. Subscribers click through to the post page for
	// the gated remainder. Free-tier vs paid-tier email differentiation is
	// out of scope for v1; everyone gets the same email body.
	splitR := render.SplitNewsletterPost(resolvedHTML, render.ViewerState{
		// Pretend "subscribed_free" so the subscriber break clears; the paywall
		// break still hides paid content from the email body.
		SubscribedFree: true,
	})
	bodyHTML := splitR.VisibleHTML
	if bodyHTML == "" {
		bodyHTML = resolvedHTML
	}
	// Rewrite outbound links through the click-tracker. Per-recipient
	// substitution of {{SUB_PUBLIC_ID}} happens later in personalizeEmail so
	// every subscriber lands on a unique tracked URL.
	bodyHTML = rewriteLinksForTracking(bodyHTML, post.PublicId)
	bodyHTML = wrapEmailHTML(post, bodyHTML)

	count := 0
	for _, sub := range subs {
		var msg *pkgmodels.Email
		if scheduled {
			msg = pkgmodels.NewScheduledEmail()
			msg.Scheduled = &scheduledAt
		} else {
			msg = pkgmodels.NewInstantEmail()
		}
		msg.From = from
		msg.To = sub.Email
		msg.SubjectLine = subject
		msg.Html = personalizeEmail(bodyHTML, sub)
		msg.ReplyTo = cfg.ReplyToEmail

		col := pkgmodels.InstantEmailCollection
		if scheduled {
			col = pkgmodels.ScheduledEmailCollection
		}
		if err := db.GetCollection(col).Insert(msg); err != nil {
			continue
		}
		count++

		// Send instant rows immediately via SMTP (MailHog in dev). Scheduled
		// rows are picked up by the in-process scheduler at Scheduled time.
		if !scheduled && smtpProvider != nil {
			_ = smtpProvider.SendEmail(msg.From, msg.To, msg.SubjectLine, msg.Html, msg.ReplyTo)
		}
	}

	if count > 0 {
		_ = db.GetCollection(pkgmodels.NewsletterPostCollection).Update(
			bson.M{"_id": post.Id},
			bson.M{"$inc": bson.M{"stats.emails_sent": int64(count)}},
		)
	}
	return count, nil
}

// wrapEmailHTML adds a minimal email shell (header, body, unsubscribe
// footer, open tracking pixel) around the post body. {{UNSUBSCRIBE_URL}}
// and {{SUB_PUBLIC_ID}} are placeholders substituted per-recipient by
// personalizeEmail at send time.
func wrapEmailHTML(post *pkgmodels.NewsletterPost, body string) string {
	preview := post.EmailPreviewText
	header := ""
	if post.Title != "" {
		header = fmt.Sprintf(`<h1 style="font-family:Georgia,serif;color:#111">%s</h1>`, htmlEscape(post.Title))
	}
	if post.Subtitle != "" {
		header += fmt.Sprintf(`<p style="color:#555;font-size:16px">%s</p>`, htmlEscape(post.Subtitle))
	}
	// Tracking pixel — emitted last in the body so most clients have already
	// fired all other resource loads by the time they reach it. Width/height
	// 1px so it renders silently. Some clients block remote images by default
	// — that's an accepted floor for open-rate accuracy.
	pixel := fmt.Sprintf(`<img src="/api/marketing/newsletters/track/open?p=%s&s={{SUB_PUBLIC_ID}}" width="1" height="1" alt="" style="display:block;width:1px;height:1px;border:0">`, post.PublicId)
	footer := `<hr style="margin:40px 0;border:none;border-top:1px solid #ddd"/>
<p style="color:#888;font-size:12px;text-align:center">You're receiving this because you subscribed.
<a href="{{UNSUBSCRIBE_URL}}" style="color:#888">Unsubscribe</a></p>`
	hidden := ""
	if preview != "" {
		hidden = fmt.Sprintf(`<div style="display:none;max-height:0;overflow:hidden">%s</div>`, htmlEscape(preview))
	}
	return fmt.Sprintf(`<html><body style="font-family:Helvetica,Arial,sans-serif;line-height:1.6;color:#222;max-width:640px;margin:0 auto;padding:24px">
%s
%s
%s
%s
%s
</body></html>`, hidden, header, body, footer, pixel)
}

// linkHrefRE matches the href attribute of an anchor tag. Captures the URL.
// Permissive on whitespace; tolerant of single or double quotes.
var linkHrefRE = regexp.MustCompile(`(?i)<a\s+([^>]*?)href\s*=\s*"([^"]+)"([^>]*)>`)

// rewriteLinksForTracking wraps every <a href="..."> in the body so it points
// at the click tracker. Excludes mailto:, tel:, and #fragment links — those
// don't open a browser request we can intercept. The {{SUB_PUBLIC_ID}}
// placeholder is filled in per-recipient at send time.
func rewriteLinksForTracking(body, postPublicID string) string {
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
		tracked := fmt.Sprintf("/api/marketing/newsletters/track/click?p=%s&s={{SUB_PUBLIC_ID}}&u=%s",
			postPublicID, urlQueryEscape(href))
		return fmt.Sprintf(`<a %shref="%s"%s>`, pre, tracked, post)
	})
}

func urlQueryEscape(s string) string {
	// Avoid net/url import bloat for one call.
	r := strings.NewReplacer(
		"%", "%25",
		"&", "%26",
		"=", "%3D",
		" ", "%20",
		"?", "%3F",
		"#", "%23",
		`"`, "%22",
	)
	return r.Replace(s)
}

func personalizeEmail(html string, sub pkgmodels.NewsletterSubscription) string {
	// Build the unsubscribe URL using a stable host hint. In v1 this is just
	// path-only; the Caddyfile rewrites Host so the host header is always
	// the tenant domain when serving public mail-action endpoints.
	unsub := fmt.Sprintf("/api/marketing/newsletters/unsubscribe?token=%s", sub.UnsubscribeToken)
	out := strings.ReplaceAll(html, "{{UNSUBSCRIBE_URL}}", unsub)
	out = strings.ReplaceAll(out, "{{SUB_PUBLIC_ID}}", sub.PublicId)
	return out
}
