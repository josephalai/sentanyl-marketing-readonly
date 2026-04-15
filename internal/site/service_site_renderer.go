package site

import (
	"fmt"

	"gopkg.in/mgo.v2/bson"
)

// ServiceResolvePublicPage resolves a public page request by domain and path.
// Returns the published HTML snapshot for serving.
func ServiceResolvePublicPage(domain, path string) (string, error) {
	site, err := FindSiteByDomain(domain)
	if err != nil {
		return "", fmt.Errorf("site not found for domain %s: %w", domain, err)
	}

	var page *SitePage
	if path == "" || path == "/" {
		page, err = FindHomePageForSite(site.Id)
	} else {
		page, err = FindPublishedPageBySlug(site.Id, path)
	}
	if err != nil {
		return "", fmt.Errorf("page not found: %w", err)
	}

	if page.PublishedHTML == "" {
		return "", fmt.Errorf("page has no published snapshot")
	}

	return page.PublishedHTML, nil
}

// ServiceAttachDomain attaches a domain to a site's attached_domains list.
func ServiceAttachDomain(siteID, tenantID bson.ObjectId, domain string) error {
	site, err := GetSiteByID(siteID, tenantID)
	if err != nil {
		return fmt.Errorf("site not found: %w", err)
	}
	// Check if domain already attached.
	for _, d := range site.AttachedDomains {
		if d == domain {
			return nil // Already attached
		}
	}
	domains := append(site.AttachedDomains, domain)
	return UpdateSite(siteID, tenantID, bson.M{
		"attached_domains": domains,
	})
}
