package site

import (
	"fmt"
	"time"

	"gopkg.in/mgo.v2/bson"
)

// ServicePublishPage creates a published snapshot from the current draft.
func ServicePublishPage(pageID, tenantID bson.ObjectId) (string, error) {
	page, err := GetSitePageByID(pageID, tenantID)
	if err != nil {
		return "", fmt.Errorf("page not found: %w", err)
	}
	if page.DraftDocument == nil {
		return "", fmt.Errorf("no draft document to publish")
	}

	// Get parent site for context.
	site, _ := GetSiteByID(page.SiteID, tenantID)

	// Render the HTML snapshot.
	html := RenderPuckDocumentToHTML(page.DraftDocument, page.SEO, site)

	// Create a published version.
	latestVer, _ := GetLatestVersionNumber(pageID, tenantID)
	version := NewSitePageVersion(page.SiteID, pageID, tenantID, VersionTypePublished, latestVer+1)
	version.PuckRoot = page.DraftDocument
	version.RenderedHTML = html
	version.SEO = page.SEO
	version.Metadata = &SiteVersionMetadata{GeneratedBy: "publish"}
	if err := CreateSitePageVersion(version); err != nil {
		return "", fmt.Errorf("failed to create published version: %w", err)
	}

	// Update the page with published state.
	now := time.Now()
	_ = UpdateSitePage(pageID, tenantID, bson.M{
		"status":               "published",
		"published_version_id": version.PublicId,
		"published_html":       html,
	})

	// Update site status too.
	if site != nil {
		_ = UpdateSite(site.Id, tenantID, bson.M{
			"status":       "published",
			"published_at": now,
		})
	}

	return html, nil
}

// ServiceRestoreVersion restores a page's draft from a previous version.
// Updates both the draft document and the draft_version_id pointer.
func ServiceRestoreVersion(pageID, versionID, tenantID bson.ObjectId) error {
	version, err := GetVersionByID(versionID, tenantID)
	if err != nil {
		return fmt.Errorf("version not found: %w", err)
	}

	// Create a new draft snapshot version for the restore.
	latestVer, _ := GetLatestVersionNumber(pageID, tenantID)
	newVersion := NewSitePageVersion(version.SiteID, pageID, tenantID, VersionTypeDraft, latestVer+1)
	newVersion.PuckRoot = version.PuckRoot
	newVersion.SEO = version.SEO
	newVersion.Metadata = &SiteVersionMetadata{GeneratedBy: "restore"}
	if err := CreateSitePageVersion(newVersion); err != nil {
		return fmt.Errorf("failed to create restore version: %w", err)
	}

	return UpdateSitePage(pageID, tenantID, bson.M{
		"draft_document":   version.PuckRoot,
		"seo":              version.SEO,
		"draft_version_id": newVersion.PublicId,
	})
}
