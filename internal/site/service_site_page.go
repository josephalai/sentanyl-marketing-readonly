package site

import (
	"fmt"

	"gopkg.in/mgo.v2/bson"
)

// PageCreateRequest holds input for creating a new page.
type PageCreateRequest struct {
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	IsHome       bool   `json:"is_home,omitempty"`
	StarterKitID string `json:"starter_kit_id,omitempty"` // pre-seeds DraftDocument from a starter kit
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
	// Flatten Section/Container wrappers before validation — Puck's editor
	// has no UI for nested-array `content` props, so a Section wrapping a
	// RichTextSection becomes uneditable. Flattening hoists the children to
	// the parent level while stamping the wrapper's tone/padding onto each
	// child. The published HTML is visually identical.
	if content, ok := document["content"].([]any); ok {
		document["content"] = flattenContainerWrappers(content)
	}

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
// Container wrappers (Section/Container) are flattened on read so the
// editor can reach every block. The Save path re-flattens defensively.
func ServiceGetDocument(pageID, tenantID bson.ObjectId) (map[string]any, error) {
	page, err := GetSitePageByID(pageID, tenantID)
	if err != nil {
		return nil, err
	}
	doc := page.DraftDocument
	if doc != nil {
		if content, ok := doc["content"].([]any); ok {
			doc["content"] = flattenContainerWrappers(content)
		}
	}
	return doc, nil
}

// flattenContainerWrappers unwraps Section and Container nodes by hoisting
// their nested children to the parent level. Each child inherits any
// tone / paddingY / maxWidth / backgroundImage from the wrapper that the
// child doesn't already define — so the published page renders the same
// band semantics, but every block becomes a first-class top-level node
// the Puck editor can select and edit.
//
// Stack and Grid intentionally stay intact: their nested layout has
// semantic meaning (vertical-gap stack / multi-column grid) that
// flattening would destroy. Those will be addressed when the Puck SSR
// renderer lands and we can use real DropZones.
func flattenContainerWrappers(nodes []any) []any {
	out := make([]any, 0, len(nodes))
	for _, raw := range nodes {
		node := coerceMap(raw)
		if node == nil {
			out = append(out, raw)
			continue
		}
		t, _ := node["type"].(string)
		if t != "Section" && t != "Container" {
			out = append(out, node)
			continue
		}
		props := coerceMap(node["props"])
		children, _ := props["content"].([]any)
		if len(children) == 0 {
			// Empty wrapper — drop it.
			continue
		}
		// Stamp wrapper styling onto each child if the child doesn't set it.
		for _, childRaw := range children {
			child := coerceMap(childRaw)
			if child == nil {
				out = append(out, childRaw)
				continue
			}
			cprops, _ := child["props"].(map[string]any)
			if cprops == nil {
				cprops = map[string]any{}
				child["props"] = cprops
			}
			for _, k := range []string{"tone", "paddingY", "maxWidth", "backgroundImage"} {
				if v, ok := props[k]; ok && v != nil && v != "" {
					if existing, exists := cprops[k]; !exists || existing == nil || existing == "" {
						cprops[k] = v
					}
				}
			}
			out = append(out, child)
		}
	}
	// Recurse: a Section may have wrapped another Section (rare but possible).
	// Run flatten until stable, capped at 4 passes to avoid pathological cases.
	for pass := 0; pass < 4; pass++ {
		changed := false
		for _, n := range out {
			if m := coerceMap(n); m != nil {
				if t, _ := m["type"].(string); t == "Section" || t == "Container" {
					changed = true
					break
				}
			}
		}
		if !changed {
			break
		}
		out = flattenContainerWrappers(out)
	}
	return out
}
