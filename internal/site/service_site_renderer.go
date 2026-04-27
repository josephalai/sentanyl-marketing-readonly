package site

import (
	"fmt"
	"html"
	"strings"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// ServiceResolvePublicPage resolves a public page request by domain and path.
// Implements the fallback cascade per spec:
//  1. site page
//  2. funnel route
//  3. offer page
//
// Returns the HTML content for serving.
func ServiceResolvePublicPage(domain, path string) (string, error) {
	// Normalize path.
	if path == "" {
		path = "/"
	}

	// 1. Try site page first.
	site, err := FindSiteByDomain(domain)
	if err == nil {
		var page *SitePage
		if path == "/" {
			page, err = FindHomePageForSite(site.Id)
		} else {
			page, err = FindPublishedPageBySlug(site.Id, path)
		}
		if err == nil && page != nil && page.PublishedHTML != "" {
			return page.PublishedHTML, nil
		}
	}

	// 2. Try funnel route — look for a funnel whose domain matches.
	funnelHTML, err := resolveFunnelPageByDomain(domain, path)
	if err == nil && funnelHTML != "" {
		return funnelHTML, nil
	}

	// 3. Try offer by public_id in the path.
	offerHTML, err := resolveOfferPageByPath(domain, path)
	if err == nil && offerHTML != "" {
		return offerHTML, nil
	}

	// 4. Newsletter homepage / post page. Path forms:
	//    /newsletter           → homepage with subscribe form + post list
	//    /newsletter/<slug>    → post detail page
	if strings.HasPrefix(path, "/newsletter") {
		nlHTML, err := resolveNewsletterPageByPath(domain, path)
		if err == nil && nlHTML != "" {
			return nlHTML, nil
		}
	}

	return "", fmt.Errorf("no content found for %s%s", domain, path)
}

// resolveNewsletterPageByPath renders the public newsletter homepage or a
// public post page. Anonymous viewers see content above the subscriber-break;
// the page also embeds the subscribe form for double-opt-in capture.
func resolveNewsletterPageByPath(domain, path string) (string, error) {
	s, err := FindSiteByDomain(domain)
	if err != nil {
		return "", err
	}

	// Find the first newsletter product on this tenant. Tenants in v1 ship
	// one newsletter; multi-newsletter routing layers in later by adding a
	// /newsletter/<newsletter-slug>/ segment.
	var product pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"tenant_id":             s.TenantID,
		"product_type":          pkgmodels.ProductTypeNewsletter,
		"timestamps.deleted_at": nil,
	}).One(&product); err != nil {
		return "", fmt.Errorf("newsletter not found")
	}

	// Homepage.
	if path == "/newsletter" || path == "/newsletter/" {
		var posts []pkgmodels.NewsletterPost
		_ = db.GetCollection(pkgmodels.NewsletterPostCollection).Find(bson.M{
			"tenant_id":             s.TenantID,
			"product_id":            product.Id,
			"status":                pkgmodels.NewsletterPostStatusPublished,
			"hide_from_web":         false,
			"timestamps.deleted_at": nil,
		}).Sort("-published_at").Limit(50).All(&posts)
		return renderNewsletterHome(&product, posts), nil
	}

	// Post detail.
	slug := strings.TrimPrefix(path, "/newsletter/")
	slug = strings.TrimSuffix(slug, "/")
	if slug == "" {
		return "", fmt.Errorf("missing slug")
	}
	var post pkgmodels.NewsletterPost
	if err := db.GetCollection(pkgmodels.NewsletterPostCollection).Find(bson.M{
		"tenant_id":             s.TenantID,
		"product_id":            product.Id,
		"slug":                  slug,
		"status":                pkgmodels.NewsletterPostStatusPublished,
		"hide_from_web":         false,
		"timestamps.deleted_at": nil,
	}).One(&post); err != nil {
		return "", fmt.Errorf("post not found")
	}

	// Bump impressions opportunistically.
	_ = db.GetCollection(pkgmodels.NewsletterPostCollection).Update(
		bson.M{"_id": post.Id},
		bson.M{"$inc": bson.M{"stats.impressions": 1}},
	)

	return renderNewsletterPostPage(&product, &post), nil
}

func renderNewsletterHome(product *pkgmodels.Product, posts []pkgmodels.NewsletterPost) string {
	cfg := product.Newsletter
	if cfg == nil {
		cfg = &pkgmodels.NewsletterConfig{}
	}
	var sb strings.Builder
	sb.WriteString(`<!doctype html><html><head><meta charset="utf-8">`)
	sb.WriteString(fmt.Sprintf(`<title>%s</title>`, html.EscapeString(product.Name)))
	sb.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	sb.WriteString(newsletterCSS())
	sb.WriteString(`</head><body>`)
	sb.WriteString(`<div class="nl-container">`)
	sb.WriteString(fmt.Sprintf(`<header class="nl-hero"><h1>%s</h1>`, html.EscapeString(product.Name)))
	if cfg.Tagline != "" {
		sb.WriteString(fmt.Sprintf(`<p class="nl-tagline">%s</p>`, html.EscapeString(cfg.Tagline)))
	}
	if cfg.Description != "" {
		sb.WriteString(fmt.Sprintf(`<p>%s</p>`, html.EscapeString(cfg.Description)))
	}
	sb.WriteString(newsletterSubscribeFormHTML(product))
	sb.WriteString(`</header>`)

	sb.WriteString(`<section class="nl-posts"><h2>Latest issues</h2>`)
	if len(posts) == 0 {
		sb.WriteString(`<p>No issues yet — subscribe to be the first to read.</p>`)
	} else {
		sb.WriteString(`<ul class="nl-list">`)
		for _, p := range posts {
			sb.WriteString(`<li class="nl-card">`)
			sb.WriteString(fmt.Sprintf(`<a href="/newsletter/%s"><h3>%s</h3></a>`, html.EscapeString(p.Slug), html.EscapeString(p.Title)))
			if p.Subtitle != "" {
				sb.WriteString(fmt.Sprintf(`<p>%s</p>`, html.EscapeString(p.Subtitle)))
			}
			sb.WriteString(`</li>`)
		}
		sb.WriteString(`</ul>`)
	}
	sb.WriteString(`</section></div></body></html>`)
	return sb.String()
}

func renderNewsletterPostPage(product *pkgmodels.Product, post *pkgmodels.NewsletterPost) string {
	// Anonymous viewer state — public page, so split at subscriber-break.
	var visibleHTML string
	subIdx := strings.Index(post.RenderedHTML, "<!--subscriber-break-->")
	if subIdx >= 0 {
		visibleHTML = post.RenderedHTML[:subIdx]
	} else {
		visibleHTML = post.RenderedHTML
	}

	var sb strings.Builder
	sb.WriteString(`<!doctype html><html><head><meta charset="utf-8">`)
	title := post.SEOTitle
	if title == "" {
		title = post.Title
	}
	sb.WriteString(fmt.Sprintf(`<title>%s</title>`, html.EscapeString(title)))
	if post.SEODescription != "" {
		sb.WriteString(fmt.Sprintf(`<meta name="description" content="%s">`, html.EscapeString(post.SEODescription)))
	}
	sb.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	sb.WriteString(newsletterCSS())
	sb.WriteString(`</head><body><div class="nl-container">`)
	sb.WriteString(`<nav class="nl-nav"><a href="/newsletter">← Back to newsletter</a></nav>`)
	sb.WriteString(`<article class="nl-post">`)
	sb.WriteString(fmt.Sprintf(`<h1>%s</h1>`, html.EscapeString(post.Title)))
	if post.Subtitle != "" {
		sb.WriteString(fmt.Sprintf(`<p class="nl-subtitle">%s</p>`, html.EscapeString(post.Subtitle)))
	}
	sb.WriteString(`<div class="nl-body">`)
	sb.WriteString(visibleHTML)
	sb.WriteString(`</div>`)
	if subIdx >= 0 {
		sb.WriteString(`<div class="nl-gate">`)
		sb.WriteString(`<h3>Subscribe to keep reading</h3>`)
		sb.WriteString(`<p>The rest of this post is for subscribers. Enter your email to read on.</p>`)
		sb.WriteString(newsletterSubscribeFormHTML(product))
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`</article></div></body></html>`)
	return sb.String()
}

func newsletterSubscribeFormHTML(product *pkgmodels.Product) string {
	return fmt.Sprintf(`<form class="nl-subscribe" data-newsletter-id="%s" onsubmit="submitNewsletterForm(event,this);return false;">
  <input type="email" name="email" placeholder="you@example.com" required style="padding:12px;border:1px solid #ccc;border-radius:6px;width:260px;max-width:100%%">
  <button type="submit" class="nl-cta">Subscribe</button>
  <div class="nl-msg" data-role="msg" style="margin-top:10px;color:#0a7"></div>
</form>
<script>
function submitNewsletterForm(ev, f){
  ev.preventDefault();
  var email = f.email.value.trim();
  var msg = f.querySelector('[data-role=msg]');
  msg.textContent = 'Submitting…';
  fetch('/api/marketing/newsletters/subscribe', {
    method:'POST', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({email: email, product_id: f.getAttribute('data-newsletter-id'), domain: location.host})
  }).then(function(r){ return r.json().then(function(j){ return {status: r.status, body: j}; }); })
    .then(function(res){
      var j = res.body;
      if (res.status === 202 || j.status === 'pending_confirmation') {
        msg.textContent = 'Check your inbox to confirm your subscription.';
      } else if (j.status === 'subscribed' || j.status === 'already_subscribed') {
        msg.textContent = 'Subscribed.';
      } else {
        msg.textContent = j.error || 'Subscription failed';
        msg.style.color = '#c33';
      }
    })
    .catch(function(){ msg.textContent = 'Network error'; msg.style.color = '#c33'; });
  return false;
}
</script>`, product.Id.Hex())
}

func newsletterCSS() string {
	return `<style>
body{font-family:Georgia,serif;color:#111;background:#fafafa;margin:0}
.nl-container{max-width:720px;margin:0 auto;padding:48px 20px}
.nl-hero{text-align:center;padding-bottom:32px;border-bottom:1px solid #eee;margin-bottom:32px}
.nl-hero h1{font-size:2.4rem;margin:0 0 8px}
.nl-tagline{color:#555;font-size:1.1rem;margin:0 0 16px}
.nl-cta{padding:12px 24px;background:#111;color:#fff;border:none;border-radius:6px;font-weight:600;cursor:pointer;margin-left:8px}
.nl-list{list-style:none;padding:0}
.nl-card{padding:20px 0;border-bottom:1px solid #eee}
.nl-card a{color:#111;text-decoration:none}
.nl-card h3{margin:0 0 6px;font-size:1.4rem}
.nl-nav{margin-bottom:24px}
.nl-nav a{color:#666;text-decoration:none}
.nl-post h1{font-size:2.2rem;margin:0 0 8px}
.nl-subtitle{color:#666;font-size:1.1rem;margin:0 0 24px}
.nl-body{font-size:1.1rem;line-height:1.7}
.nl-gate{margin-top:48px;padding:32px;background:#fff;border:2px solid #111;border-radius:12px;text-align:center}
.nl-gate h3{margin:0 0 8px}
.newsletter-gate{margin-top:48px;padding:32px;background:#fff;border:2px solid #111;border-radius:12px;text-align:center}
.newsletter-gate-cta{display:inline-block;padding:12px 24px;background:#111;color:#fff;border-radius:6px;text-decoration:none;font-weight:600;margin-top:12px}
</style>`
}

// resolveFunnelPageByDomain tries to find a funnel page by domain and path.
func resolveFunnelPageByDomain(domain, path string) (string, error) {
	var funnel pkgmodels.Funnel
	err := db.GetCollection(pkgmodels.FunnelCollection).Find(bson.M{
		"domain":                domain,
		"timestamps.deleted_at": nil,
	}).One(&funnel)
	if err != nil {
		return "", err
	}
	// Find funnel routes for this funnel.
	var routes []pkgmodels.FunnelRoute
	err = db.GetCollection(pkgmodels.FunnelRouteCollection).Find(bson.M{
		"funnel_id":             funnel.Id,
		"timestamps.deleted_at": nil,
	}).Sort("order").All(&routes)
	if err != nil || len(routes) == 0 {
		return "", fmt.Errorf("no funnel routes found")
	}
	// For the root path, serve the first route's first page.
	// Find stage IDs from routes.
	for _, route := range routes {
		if route.StageIds != nil {
			for _, stageID := range route.StageIds.Ids {
				var page pkgmodels.FunnelPage
				err = db.GetCollection(pkgmodels.FunnelPageCollection).Find(bson.M{
					"stage_id":              stageID,
					"timestamps.deleted_at": nil,
				}).One(&page)
				if err == nil && page.RenderedHTML != "" {
					return page.RenderedHTML, nil
				}
			}
		}
	}
	return "", fmt.Errorf("funnel page has no rendered content")
}

// resolveOfferPageByPath tries to match a path segment to an offer public_id.
func resolveOfferPageByPath(domain string, path string) (string, error) {
	slug := strings.TrimPrefix(path, "/offer/")
	if slug == path || slug == "" {
		return "", fmt.Errorf("not an offer path")
	}
	var offer pkgmodels.Offer
	err := db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
		"public_id":             slug,
		"timestamps.deleted_at": nil,
	}).One(&offer)
	if err != nil {
		return "", err
	}
	// Generate a simple offer landing page.
	var sb strings.Builder
	sb.WriteString("<!DOCTYPE html><html><head><meta charset=\"UTF-8\">")
	sb.WriteString(fmt.Sprintf("<title>%s</title>", html.EscapeString(offer.Title)))
	sb.WriteString("<style>body{font-family:sans-serif;text-align:center;padding:60px 20px} .btn{display:inline-block;padding:12px 32px;background:#4f46e5;color:white;text-decoration:none;border-radius:8px;font-weight:600;margin-top:1rem}</style>")
	sb.WriteString("</head><body>")
	sb.WriteString(fmt.Sprintf("<h1>%s</h1>", html.EscapeString(offer.Title)))
	if offer.Amount > 0 {
		sb.WriteString(fmt.Sprintf("<p style=\"font-size:2rem;font-weight:bold\">$%.2f %s</p>", float64(offer.Amount)/100, html.EscapeString(strings.ToUpper(offer.Currency))))
	}
	sb.WriteString("<a class=\"btn\" href=\"/api/marketing/site/checkout/start\">Buy Now</a>")
	sb.WriteString("</body></html>")
	return sb.String(), nil
}

// ServiceAttachDomain attaches a verified domain to a site's attached_domains list.
// The domain must exist in the platform's domain system (tenant_domains collection)
// and be verified before it can be attached to a website.
func ServiceAttachDomain(siteID, tenantID bson.ObjectId, domainID string) error {
	site, err := GetSiteByID(siteID, tenantID)
	if err != nil {
		return fmt.Errorf("site not found: %w", err)
	}

	// Look up the domain in the platform domain system.
	var tenantDomain pkgmodels.TenantDomain
	if bson.IsObjectIdHex(domainID) {
		err = db.GetCollection(pkgmodels.DomainCollection).Find(bson.M{
			"_id":                   bson.ObjectIdHex(domainID),
			"tenant_id":             tenantID,
			"timestamps.deleted_at": nil,
		}).One(&tenantDomain)
	}
	if err != nil {
		// Fallback: try by hostname.
		err = db.GetCollection(pkgmodels.DomainCollection).Find(bson.M{
			"hostname":              domainID,
			"tenant_id":             tenantID,
			"timestamps.deleted_at": nil,
		}).One(&tenantDomain)
		if err != nil {
			return fmt.Errorf("domain not found in your account — add it via Domains first")
		}
	}

	if !tenantDomain.IsVerified {
		return fmt.Errorf("domain %s is not verified — verify DNS records first", tenantDomain.Hostname)
	}

	hostname := tenantDomain.Hostname

	// Check if domain already attached.
	for _, d := range site.AttachedDomains {
		if d == hostname {
			return nil // Already attached
		}
	}
	domains := append(site.AttachedDomains, hostname)
	return UpdateSite(siteID, tenantID, bson.M{
		"attached_domains": domains,
	})
}

// VerifyDomainForTLS checks that the given hostname is currently attached to
// a published site, and that the domain is verified in the platform domain
// system. This is used by Caddy's on-demand TLS validation endpoint.
func VerifyDomainForTLS(hostname string) error {
	// 1. Check that a published site has this domain attached.
	_, err := FindSiteByDomain(hostname)
	if err != nil {
		return fmt.Errorf("no published site for domain %s", hostname)
	}

	// 2. Verify the domain is registered and verified in tenant_domains.
	var tenantDomain pkgmodels.TenantDomain
	err = db.GetCollection(pkgmodels.DomainCollection).Find(bson.M{
		"hostname":              hostname,
		"is_verified":           true,
		"timestamps.deleted_at": nil,
	}).One(&tenantDomain)
	if err != nil {
		return fmt.Errorf("domain %s not verified in platform", hostname)
	}

	return nil
}
