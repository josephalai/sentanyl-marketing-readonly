// Package scheduler runs the newsletter dispatch pass:
//
//   1. Absolute-mode publish — flips scheduled posts to published at their
//      ScheduledAt and fans out the broadcast to all active subscribers.
//   2. Drip-mode dispatch — for each drip post in status=published, finds
//      every active subscription whose elapsed-since-subscribe has crossed
//      that post's DripOffsetSeconds, and inserts one email per missing
//      (post, subscription) pair. The newsletter_drip_dispatches collection
//      dedupes — unique on (post_id, subscription_id) — so retries are safe.
//
// W3-B / OPS-001: the pass runs as a durable self-rescheduling job on
// pkg/jobs (was an in-process goroutine), so it survives restarts and the
// job lease guarantees a single dispatcher across replicas (OPS-004).
package scheduler

import (
	"context"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/email"
	"github.com/josephalai/sentanyl/marketing-service/internal/ai"
	"github.com/josephalai/sentanyl/marketing-service/routes"
	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/emailer"
	"github.com/josephalai/sentanyl/pkg/jobs"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/render"
)

// in-memory dedupe set for the current process. Catches restart-window
// double-fires; the (post, subscription) unique mongo index is the durable
// authority.
var (
	inMemMu  sync.Mutex
	inMemSet = make(map[string]struct{})
)

// smtp is the provider the in-process scheduler uses for drip sends.
// Same constructor pattern as routes/email.go's init.
var smtp email.EmailProvider

const newsletterSweepJobType = "newsletter.sweep"

// DripEmailRenderer is injected by cmd/main from the routes package so drip
// sends share the broadcast pipeline exactly (send authority, signed
// tracking, personalization, compliance headers, EmailSend row).
var DripEmailRenderer func(product *pkgmodels.Product, post *pkgmodels.NewsletterPost, sub pkgmodels.NewsletterSubscription, preSplitBody, subject string) (html string, headers map[string]string, send *pkgmodels.EmailSend, allowed bool, reason string)

// Start registers the newsletter sweep job and bootstraps the chain. Interval
// defaults to 60s and is overridable with NEWSLETTER_SCHEDULER_INTERVAL
// (seconds). The jobs worker (already running in marketing-service main)
// executes the sweeps; panics are recovered by the worker and dead-lettered
// after retries instead of silently killing a ticker.
func Start() {
	if os.Getenv("EMAIL_PROVIDER") != "" {
		smtp = email.DefaultProvider()
	}

	interval := 60 * time.Second
	if v := os.Getenv("NEWSLETTER_SCHEDULER_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			interval = time.Duration(n) * time.Second
		}
	}

	jobs.Register(newsletterSweepJobType, func(ctx context.Context, job *jobs.Job) error {
		// Re-arm the chain FIRST so a crash mid-sweep never stalls dispatch.
		if err := jobs.EnqueueSweep(newsletterSweepJobType, time.Now().Add(interval), interval); err != nil {
			return err
		}
		tick(time.Now().UTC())
		if job.RunAt.Unix()%3600 < int64(interval/time.Second) {
			jobs.PruneSucceeded(newsletterSweepJobType, 24*time.Hour)
		}
		return nil
	})
	if err := jobs.EnqueueSweep(newsletterSweepJobType, time.Now(), interval); err != nil {
		log.Printf("[newsletter scheduler] bootstrap sweep enqueue failed: %v", err)
	}
	log.Printf("[newsletter scheduler] durable sweep registered (%s interval)", interval)
}

func tick(now time.Time) {
	publishDueAbsolutePosts(now)
	dispatchDueDripEmails(now)
}

// publishDueAbsolutePosts is the absolute-mode part of the tick. Scoped by
// status + schedule_mode so we never re-publish an already-published post
// or accidentally treat a drip post as absolute.
func publishDueAbsolutePosts(now time.Time) {
	var posts []pkgmodels.NewsletterPost
	err := db.GetCollection(pkgmodels.NewsletterPostCollection).Find(bson.M{
		"status": pkgmodels.NewsletterPostStatusScheduled,
		"$and": []bson.M{
			{"$or": []bson.M{
				{"schedule_mode": pkgmodels.NewsletterScheduleAbsolute},
				{"schedule_mode": ""},
				{"schedule_mode": bson.M{"$exists": false}},
			}},
		},
		"scheduled_at":          bson.M{"$lte": now},
		"timestamps.deleted_at": nil,
	}).Limit(50).All(&posts)
	if err != nil {
		log.Printf("[newsletter scheduler] absolute scan: %v", err)
		return
	}
	for _, post := range posts {
		var product pkgmodels.Product
		if err := db.GetCollection(pkgmodels.ProductCollection).FindId(post.ProductID).One(&product); err != nil {
			log.Printf("[newsletter scheduler] product lookup for post %s: %v", post.PublicId, err)
			continue
		}
		sent, err := routes.PublishPostNow(&product, &post, false, time.Time{})
		if err != nil {
			log.Printf("[newsletter scheduler] publish post %s: %v", post.PublicId, err)
			continue
		}
		log.Printf("[newsletter scheduler] auto-published post %s (%d emails)", post.PublicId, sent)
	}
}

// dispatchDueDripEmails is the drip-mode part of the tick. For every
// drip post in status=published we find each active subscription whose
// (now - subscribed_at) has crossed the post's drip offset AND has no
// existing newsletter_drip_dispatches row, then insert exactly one email.
//
// The unique mongo index on (post_id, subscription_id) makes retries
// idempotent if the worker crashes between dispatch insert and email
// insert.
func dispatchDueDripEmails(now time.Time) {
	var dripPosts []pkgmodels.NewsletterPost
	err := db.GetCollection(pkgmodels.NewsletterPostCollection).Find(bson.M{
		"schedule_mode":         pkgmodels.NewsletterScheduleDrip,
		"status":                pkgmodels.NewsletterPostStatusPublished,
		"timestamps.deleted_at": nil,
	}).All(&dripPosts)
	if err != nil {
		log.Printf("[newsletter scheduler] drip scan: %v", err)
		return
	}
	for _, post := range dripPosts {
		var product pkgmodels.Product
		if err := db.GetCollection(pkgmodels.ProductCollection).FindId(post.ProductID).One(&product); err != nil {
			continue
		}
		dispatchDripPost(&product, &post, now)
	}
}

func dispatchDripPost(product *pkgmodels.Product, post *pkgmodels.NewsletterPost, now time.Time) {
	cutoff := now.Add(-time.Duration(post.DripOffsetSeconds) * time.Second)
	var subs []pkgmodels.NewsletterSubscription
	err := db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Find(bson.M{
		"tenant_id":     product.TenantID,
		"product_id":    product.Id,
		"status":        pkgmodels.NewsletterSubscriptionStatusActive,
		"subscribed_at": bson.M{"$lte": cutoff},
	}).All(&subs)
	if err != nil {
		log.Printf("[newsletter scheduler] drip subs query: %v", err)
		return
	}
	if len(subs) == 0 {
		return
	}

	// Resolve {{ai}} handlebars once for this post in this tick. The
	// resolver caches per (tenant, prompt, time-bucket), so all drip
	// recipients in the same bucket land on the same value — same
	// guarantee as a broadcast.
	resolvedHTML := post.RenderedHTML
	if resolver := ai.Resolver(); resolver != nil && product.Newsletter != nil {
		resolvedHTML = resolver.Resolve(post.RenderedHTML, render.ResolveOptions{
			TenantID:             product.TenantID,
			PostContextPackIDs:   post.ContextPackIDs,
			NewsletterDefaults:   product.Newsletter.DefaultContextPackIDs,
			NewsletterTTLSeconds: product.Newsletter.DefaultAITTLSeconds,
		})
	}

	splitR := render.SplitNewsletterPost(resolvedHTML, render.ViewerState{SubscribedFree: true})
	bodyHTML := splitR.VisibleHTML
	if bodyHTML == "" {
		bodyHTML = resolvedHTML
	}

	from := "no-reply@sentanyl.local"
	if product.Newsletter != nil && product.Newsletter.FromEmail != "" {
		from = product.Newsletter.FromEmail
	}
	subject := post.EmailSubject
	if subject == "" {
		subject = post.Title
	}

	col := db.GetCollection(pkgmodels.NewsletterDripDispatchCollection)
	for _, sub := range subs {
		dedupKey := post.Id.Hex() + "|" + sub.Id.Hex()
		inMemMu.Lock()
		if _, ok := inMemSet[dedupKey]; ok {
			inMemMu.Unlock()
			continue
		}
		inMemSet[dedupKey] = struct{}{}
		inMemMu.Unlock()

		// Mongo dedupe — the unique index on (post_id, subscription_id)
		// means this insert fails for already-dispatched pairs. We rely
		// on that error rather than a separate read.
		due := sub.SubscribedAt.Add(time.Duration(post.DripOffsetSeconds) * time.Second)
		dispatch := pkgmodels.NewNewsletterDripDispatch(product.TenantID, product.Id, post.Id, sub.Id, sub.ContactID, sub.Email, due)
		if err := col.Insert(dispatch); err != nil {
			// Likely a duplicate — already dispatched. Skip silently.
			continue
		}

		// Per-recipient render + authorization through the SAME pipeline as
		// broadcasts (COM-EM-004/006): send authority, signed tracking,
		// personalization, compliance headers, unified EmailSend row.
		if DripEmailRenderer == nil {
			log.Printf("[newsletter scheduler] drip renderer not wired; skipping send")
			continue
		}
		html, headers, sendRow, allowed, reason := DripEmailRenderer(product, post, sub, bodyHTML, subject)
		if !allowed {
			log.Printf("[newsletter scheduler] drip skip %s (reason=%s)", sub.Email, reason)
			continue
		}
		msg := pkgmodels.NewInstantEmail()
		msg.From = from
		msg.To = sub.Email
		msg.SubjectLine = subject
		msg.Html = html
		_ = db.GetCollection(pkgmodels.InstantEmailCollection).Insert(msg)
		if sendRow != nil {
			_ = db.GetCollection(pkgmodels.EmailSendCollection).Insert(sendRow)
		}
		if smtp != nil {
			if hs, ok := smtp.(emailer.HeaderSender); ok {
				_ = hs.SendEmailWithHeaders(msg.From, msg.To, msg.SubjectLine, msg.Html, "", headers)
			} else {
				_ = smtp.SendEmail(msg.From, msg.To, msg.SubjectLine, msg.Html, "")
			}
		}

		// Stamp dispatch with email message id + sent_at.
		nowSent := time.Now()
		_ = col.Update(
			bson.M{"_id": dispatch.Id},
			bson.M{"$set": bson.M{"sent_at": nowSent, "email_message_id": msg.Id}},
		)
		_ = db.GetCollection(pkgmodels.NewsletterPostCollection).Update(
			bson.M{"_id": post.Id},
			bson.M{"$inc": bson.M{"stats.emails_sent": 1}},
		)
	}
}
