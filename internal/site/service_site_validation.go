package site

import (
	"fmt"
	"regexp"
	"strings"

	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// Maximum limits for input validation.
const (
	MaxSiteNameLength     = 200
	MaxDomainLength       = 253
	MaxPageNameLength     = 200
	MaxPageSlugLength     = 500
	MaxSEOTitleLength     = 120
	MaxSEODescLength      = 320
	MaxSEOURLLength       = 2048
	MaxNavLinksPerSection = 50
	MaxNavLabelLength     = 100
	MaxNavURLLength       = 2048
	MaxPagesPerSite       = 200
	MaxDocumentDepth      = 10
	MaxComponentsPerPage  = 200
	MaxThemeLength        = 100
)

var (
	// Matches URL-safe slugs: optional leading slash, then alphanumeric/dashes/underscores.
	slugRegex   = regexp.MustCompile(`^/?[a-z0-9][a-z0-9\-_/]*$`)
	domainRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9\-]*(\.[a-zA-Z0-9][a-zA-Z0-9\-]*)*\.[a-zA-Z]{2,}$`)
)

// ValidateSiteCreate validates input for creating a new site.
func ValidateSiteCreate(req SiteCreateRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return fmt.Errorf("site name is required")
	}
	if len(req.Name) > MaxSiteNameLength {
		return fmt.Errorf("site name exceeds maximum length of %d", MaxSiteNameLength)
	}
	if req.Domain != "" {
		if err := ValidateDomain(req.Domain); err != nil {
			return err
		}
	}
	if req.Theme != "" && len(req.Theme) > MaxThemeLength {
		return fmt.Errorf("theme exceeds maximum length of %d", MaxThemeLength)
	}
	if req.SEO != nil {
		if err := ValidateSEO(req.SEO); err != nil {
			return err
		}
	}
	return nil
}

// ValidateSiteUpdate validates input for updating a site.
func ValidateSiteUpdate(req SiteUpdateRequest) error {
	if req.Name != "" && len(req.Name) > MaxSiteNameLength {
		return fmt.Errorf("site name exceeds maximum length of %d", MaxSiteNameLength)
	}
	if req.Domain != "" {
		if err := ValidateDomain(req.Domain); err != nil {
			return err
		}
	}
	if req.Theme != "" && len(req.Theme) > MaxThemeLength {
		return fmt.Errorf("theme exceeds maximum length of %d", MaxThemeLength)
	}
	if req.SEO != nil {
		if err := ValidateSEO(req.SEO); err != nil {
			return err
		}
	}
	if req.Navigation != nil {
		if err := ValidateNavigation(req.Navigation); err != nil {
			return err
		}
	}
	return nil
}

// ValidatePageCreate validates input for creating a new page.
func ValidatePageCreate(req PageCreateRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return fmt.Errorf("page name is required")
	}
	if len(req.Name) > MaxPageNameLength {
		return fmt.Errorf("page name exceeds maximum length of %d", MaxPageNameLength)
	}
	if strings.TrimSpace(req.Slug) == "" {
		return fmt.Errorf("page slug is required")
	}
	if err := ValidateSlug(req.Slug); err != nil {
		return err
	}
	return nil
}

// ValidatePageUpdate validates input for updating a page.
func ValidatePageUpdate(req PageUpdateRequest) error {
	if req.Name != "" && len(req.Name) > MaxPageNameLength {
		return fmt.Errorf("page name exceeds maximum length of %d", MaxPageNameLength)
	}
	if req.Slug != "" {
		if err := ValidateSlug(req.Slug); err != nil {
			return err
		}
	}
	return nil
}

// ValidateSlug checks that a slug is safe and well-formed.
func ValidateSlug(slug string) error {
	if len(slug) > MaxPageSlugLength {
		return fmt.Errorf("slug exceeds maximum length of %d", MaxPageSlugLength)
	}
	if slug == "/" {
		return nil // Root slug is valid
	}
	s := strings.TrimPrefix(slug, "/")
	if !slugRegex.MatchString(s) {
		return fmt.Errorf("slug must contain only lowercase letters, numbers, dashes, and underscores")
	}
	return nil
}

// ValidateDomain checks that a domain is well-formed.
func ValidateDomain(domain string) error {
	if len(domain) > MaxDomainLength {
		return fmt.Errorf("domain exceeds maximum length of %d", MaxDomainLength)
	}
	if !domainRegex.MatchString(domain) {
		return fmt.Errorf("invalid domain format")
	}
	return nil
}

// ValidateSEO validates SEO config fields.
func ValidateSEO(seo *pkgmodels.SEOConfig) error {
	if seo == nil {
		return nil
	}
	if len(seo.MetaTitle) > MaxSEOTitleLength {
		return fmt.Errorf("SEO title exceeds maximum length of %d", MaxSEOTitleLength)
	}
	if len(seo.MetaDescription) > MaxSEODescLength {
		return fmt.Errorf("SEO description exceeds maximum length of %d", MaxSEODescLength)
	}
	if len(seo.CanonicalURL) > MaxSEOURLLength {
		return fmt.Errorf("canonical URL exceeds maximum length of %d", MaxSEOURLLength)
	}
	if len(seo.OpenGraphImageURL) > MaxSEOURLLength {
		return fmt.Errorf("OG image URL exceeds maximum length of %d", MaxSEOURLLength)
	}
	return nil
}

// ValidateNavigation validates navigation config.
func ValidateNavigation(nav *pkgmodels.NavigationConfig) error {
	if nav == nil {
		return nil
	}
	if len(nav.HeaderNavLinks) > MaxNavLinksPerSection {
		return fmt.Errorf("too many header navigation links (max %d)", MaxNavLinksPerSection)
	}
	if len(nav.FooterNavLinks) > MaxNavLinksPerSection {
		return fmt.Errorf("too many footer navigation links (max %d)", MaxNavLinksPerSection)
	}
	for _, link := range nav.HeaderNavLinks {
		if err := validateNavLink(link); err != nil {
			return fmt.Errorf("header link: %w", err)
		}
	}
	for _, link := range nav.FooterNavLinks {
		if err := validateNavLink(link); err != nil {
			return fmt.Errorf("footer link: %w", err)
		}
	}
	return nil
}

func validateNavLink(link pkgmodels.NavLink) error {
	if strings.TrimSpace(link.Label) == "" {
		return fmt.Errorf("link label is required")
	}
	if len(link.Label) > MaxNavLabelLength {
		return fmt.Errorf("link label exceeds maximum length of %d", MaxNavLabelLength)
	}
	if len(link.URL) > MaxNavURLLength {
		return fmt.Errorf("link URL exceeds maximum length of %d", MaxNavURLLength)
	}
	return nil
}

// ValidatePuckDocument validates a Puck document structure.
func ValidatePuckDocument(doc map[string]any) error {
	if doc == nil {
		return fmt.Errorf("document is nil")
	}
	content, ok := doc["content"]
	if !ok {
		// A document without content is valid but empty.
		return nil
	}
	contentArr, ok := content.([]any)
	if !ok {
		return fmt.Errorf("document content must be an array")
	}
	if len(contentArr) > MaxComponentsPerPage {
		return fmt.Errorf("too many components (max %d)", MaxComponentsPerPage)
	}
	for i, item := range contentArr {
		comp, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("component at index %d is not a valid object", i)
		}
		if err := validateComponent(comp, 0); err != nil {
			return fmt.Errorf("component at index %d: %w", i, err)
		}
	}
	return nil
}

func validateComponent(comp map[string]any, depth int) error {
	if depth > MaxDocumentDepth {
		return fmt.Errorf("component nesting exceeds maximum depth of %d", MaxDocumentDepth)
	}
	compType, ok := comp["type"].(string)
	if !ok || compType == "" {
		return fmt.Errorf("component must have a 'type' string field")
	}
	if !IsKnownComponentType(compType) {
		return fmt.Errorf("unknown component type: %s", compType)
	}
	// Validate nested children in Columns.
	if compType == "Columns" {
		if props, ok := comp["props"].(map[string]any); ok {
			if cols, ok := props["columns"].([]any); ok {
				for _, col := range cols {
					if colMap, ok := col.(map[string]any); ok {
						if children, ok := colMap["children"].([]any); ok {
							for _, child := range children {
								if childComp, ok := child.(map[string]any); ok {
									if err := validateComponent(childComp, depth+1); err != nil {
										return err
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return nil
}
