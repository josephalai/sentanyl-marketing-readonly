package site

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// ResolveTenantFromDomain returns the TenantID that owns the given request
// hostname. The canonical source is `tenant_domains.is_verified=true`; the
// dev-host pattern `{public_id}.site.lvh.me` and attached site domains are
// fallbacks for environments where tenant_domains hasn't been seeded.
//
// Public-edge handlers MUST use this before resolving any tenant-scoped
// resource by `public_id` so cross-tenant ID collisions cannot resolve.
func ResolveTenantFromDomain(domain string) (bson.ObjectId, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return "", fmt.Errorf("empty domain")
	}

	// Canonical: verified tenant_domains row.
	var td pkgmodels.TenantDomain
	err := db.GetCollection(pkgmodels.DomainCollection).Find(bson.M{
		"hostname":              bson.RegEx{Pattern: "^" + regexp.QuoteMeta(domain) + "$", Options: "i"},
		"is_verified":           true,
		"timestamps.deleted_at": nil,
	}).One(&td)
	if err == nil && td.TenantID.Valid() {
		return td.TenantID, nil
	}

	// Fallback: site lookup (covers dev hosts and legacy data without a
	// tenant_domains row).
	site, err := FindSiteByDomain(domain)
	if err == nil && site != nil && site.TenantID.Valid() {
		return site.TenantID, nil
	}

	return "", fmt.Errorf("no tenant for domain %s", domain)
}
