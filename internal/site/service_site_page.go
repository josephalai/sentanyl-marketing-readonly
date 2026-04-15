package site

import (
	"fmt"

	"gopkg.in/mgo.v2/bson"
)

// PageCreateRequest holds input for creating a new page.
type PageCreateRequest struct {
	Name   string `json:"name"`
	Slug   string `json:"slug"`
	IsHome bool   `json:"is_home,omitempty"`
}

// ServiceCreatePage creates a new page for a site.
func ServiceCreatePage(req PageCreateRequest, siteID, tenantID bson.ObjectId) (*SitePage, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("page name is required")
	}
	if req.Slug == "" {
		return nil, fmt.Errorf("page slug is required")
	}
	page := NewSitePage(req.Name, req.Slug, siteID, tenantID)
	page.IsHome = req.IsHome
	if err := CreateSitePage(page); err != nil {
		return nil, fmt.Errorf("failed to create page: %w", err)
	}
	return page, nil
}

// ServiceGetPage retrieves a page by ID.
func ServiceGetPage(pageID, tenantID bson.ObjectId) (*SitePage, error) {
	return GetSitePageByID(pageID, tenantID)
}

// ServiceListPages lists all pages for a site.
func ServiceListPages(siteID, tenantID bson.ObjectId) ([]SitePage, error) {
	return ListPagesBySite(siteID, tenantID)
}

// PageUpdateRequest holds input for updating a page.
type PageUpdateRequest struct {
	Name   string `json:"name,omitempty"`
	Slug   string `json:"slug,omitempty"`
	IsHome *bool  `json:"is_home,omitempty"`
}

// ServiceUpdatePage updates page metadata.
func ServiceUpdatePage(pageID, tenantID bson.ObjectId, req PageUpdateRequest) error {
	updates := bson.M{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.Slug != "" {
		updates["slug"] = req.Slug
	}
	if req.IsHome != nil {
		updates["is_home"] = *req.IsHome
	}
	if len(updates) == 0 {
		return fmt.Errorf("no fields to update")
	}
	return UpdateSitePage(pageID, tenantID, updates)
}

// ServiceDeletePage soft-deletes a page.
func ServiceDeletePage(pageID, tenantID bson.ObjectId) error {
	return SoftDeleteSitePage(pageID, tenantID)
}

// ServiceSaveDocument saves a Puck document as the draft for a page.
func ServiceSaveDocument(pageID, tenantID bson.ObjectId, document map[string]any) error {
	return UpdateSitePage(pageID, tenantID, bson.M{
		"draft_document": document,
	})
}

// ServiceGetDocument returns the current draft document for a page.
func ServiceGetDocument(pageID, tenantID bson.ObjectId) (map[string]any, error) {
	page, err := GetSitePageByID(pageID, tenantID)
	if err != nil {
		return nil, err
	}
	return page.DraftDocument, nil
}
