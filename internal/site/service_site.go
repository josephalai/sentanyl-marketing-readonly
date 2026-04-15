package site

import (
	"fmt"

	"gopkg.in/mgo.v2/bson"

	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// SiteCreateRequest holds input for creating a new site.
type SiteCreateRequest struct {
	Name   string               `json:"name"`
	Domain string               `json:"domain,omitempty"`
	Theme  string               `json:"theme,omitempty"`
	SEO    *pkgmodels.SEOConfig `json:"seo,omitempty"`
}

// ServiceCreateSite creates a new site for a tenant.
func ServiceCreateSite(req SiteCreateRequest, tenantID bson.ObjectId) (*pkgmodels.Site, error) {
	if err := ValidateSiteCreate(req); err != nil {
		return nil, err
	}
	site := &pkgmodels.Site{
		Id:       bson.NewObjectId(),
		PublicId: utils.GeneratePublicId(),
		TenantID: tenantID,
		Name:     req.Name,
		Domain:   req.Domain,
		Theme:    req.Theme,
		Status:   "draft",
		SEO:      req.SEO,
	}
	if err := CreateSite(site); err != nil {
		return nil, fmt.Errorf("failed to create site: %w", err)
	}
	return site, nil
}

// ServiceGetSite retrieves a site by ID for a tenant.
func ServiceGetSite(siteID, tenantID bson.ObjectId) (*pkgmodels.Site, error) {
	return GetSiteByID(siteID, tenantID)
}

// ServiceListSites returns all sites for a tenant.
func ServiceListSites(tenantID bson.ObjectId) ([]pkgmodels.Site, error) {
	return ListSitesByTenant(tenantID)
}

// SiteUpdateRequest holds input for updating a site.
type SiteUpdateRequest struct {
	Name       string                     `json:"name,omitempty"`
	Domain     string                     `json:"domain,omitempty"`
	Theme      string                     `json:"theme,omitempty"`
	SEO        *pkgmodels.SEOConfig       `json:"seo,omitempty"`
	Navigation *pkgmodels.NavigationConfig `json:"navigation,omitempty"`
}

// ServiceUpdateSite updates a site.
func ServiceUpdateSite(siteID, tenantID bson.ObjectId, req SiteUpdateRequest) error {
	if err := ValidateSiteUpdate(req); err != nil {
		return err
	}
	updates := bson.M{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.Domain != "" {
		updates["domain"] = req.Domain
	}
	if req.Theme != "" {
		updates["theme"] = req.Theme
	}
	if req.SEO != nil {
		updates["seo"] = req.SEO
	}
	if req.Navigation != nil {
		updates["navigation"] = req.Navigation
	}
	if len(updates) == 0 {
		return fmt.Errorf("no fields to update")
	}
	return UpdateSite(siteID, tenantID, updates)
}

// ServiceDeleteSite soft-deletes a site and its pages.
func ServiceDeleteSite(siteID, tenantID bson.ObjectId) error {
	return SoftDeleteSite(siteID, tenantID)
}
