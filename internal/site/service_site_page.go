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
	if err := ValidatePageCreate(req); err != nil {
		return nil, err
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
	if err := ValidatePageUpdate(req); err != nil {
		return err
	}
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
// Creates a draft_snapshot version and updates the page's draft_version_id.
func ServiceSaveDocument(pageID, tenantID bson.ObjectId, document map[string]any) error {
	if err := ValidatePuckDocument(document); err != nil {
		return fmt.Errorf("document validation failed: %w", err)
	}

	// Ensure component IDs exist on each node.
	ensureComponentIDs(document)

	// Fetch the page to get siteID for version creation.
	page, err := GetSitePageByID(pageID, tenantID)
	if err != nil {
		return fmt.Errorf("page not found: %w", err)
	}

	// Create a draft snapshot version.
	latestVer, _ := GetLatestVersionNumber(pageID, tenantID)
	version := NewSitePageVersion(page.SiteID, pageID, tenantID, VersionTypeDraft, latestVer+1)
	version.PuckRoot = document
	version.SEO = page.SEO
	version.Metadata = &SiteVersionMetadata{GeneratedBy: "manual"}
	if err := CreateSitePageVersion(version); err != nil {
		return fmt.Errorf("failed to create draft version: %w", err)
	}

	// Update the page with the new draft document and version pointer.
	return UpdateSitePage(pageID, tenantID, bson.M{
		"draft_document":   document,
		"draft_version_id": version.PublicId,
	})
}

// ensureComponentIDs adds a unique ID to each component's props if one is not
// already present. This ensures patch operations can target components reliably.
func ensureComponentIDs(doc map[string]any) {
	content, ok := doc["content"].([]any)
	if !ok {
		return
	}
	for _, item := range content {
		comp, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ensureNodeID(comp)
	}
}

func ensureNodeID(comp map[string]any) {
	props, ok := comp["props"].(map[string]any)
	if !ok {
		props = map[string]any{}
		comp["props"] = props
	}
	if _, hasID := props["id"]; !hasID {
		props["id"] = bson.NewObjectId().Hex()
	}
	// Recurse into Columns children.
	if comp["type"] == "Columns" {
		if cols, ok := props["columns"].([]any); ok {
			for _, col := range cols {
				if colMap, ok := col.(map[string]any); ok {
					if children, ok := colMap["children"].([]any); ok {
						for _, child := range children {
							if childComp, ok := child.(map[string]any); ok {
								ensureNodeID(childComp)
							}
						}
					}
				}
			}
		}
	}
}

// ServiceGetDocument returns the current draft document for a page.
func ServiceGetDocument(pageID, tenantID bson.ObjectId) (map[string]any, error) {
	page, err := GetSitePageByID(pageID, tenantID)
	if err != nil {
		return nil, err
	}
	return page.DraftDocument, nil
}
