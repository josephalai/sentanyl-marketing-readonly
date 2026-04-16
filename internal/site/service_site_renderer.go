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

	return "", fmt.Errorf("no content found for %s%s", domain, path)
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
