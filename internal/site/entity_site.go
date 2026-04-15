package site

import (
	"time"

	"gopkg.in/mgo.v2/bson"

	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// SitePage represents a single page within a website.
type SitePage struct {
	Id                    bson.ObjectId        `bson:"_id" json:"id,omitempty"`
	PublicId              string               `bson:"public_id" json:"public_id,omitempty"`
	SiteID                bson.ObjectId        `bson:"site_id,omitempty" json:"site_id,omitempty"`
	TenantID              bson.ObjectId        `bson:"tenant_id,omitempty" json:"tenant_id,omitempty"`
	Name                  string               `bson:"name" json:"name,omitempty"`
	Slug                  string               `bson:"slug" json:"slug,omitempty"`
	IsHome                bool                 `bson:"is_home,omitempty" json:"is_home,omitempty"`
	Status                string               `bson:"status,omitempty" json:"status,omitempty"`
	SEO                   *pkgmodels.SEOConfig `bson:"seo,omitempty" json:"seo,omitempty"`
	DraftDocument         map[string]any       `bson:"draft_document,omitempty" json:"draft_document,omitempty"`
	DraftVersionID        string               `bson:"draft_version_id,omitempty" json:"draft_version_id,omitempty"`
	PublishedVersionID    string               `bson:"published_version_id,omitempty" json:"published_version_id,omitempty"`
	PublishedHTML         string               `bson:"published_html,omitempty" json:"published_html,omitempty"`
	LastPreviewHTML       string               `bson:"last_preview_html,omitempty" json:"last_preview_html,omitempty"`
	pkgmodels.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

// NewSitePage creates a new SitePage with generated IDs.
func NewSitePage(name, slug string, siteID, tenantID bson.ObjectId) *SitePage {
	now := time.Now()
	return &SitePage{
		Id:          bson.NewObjectId(),
		PublicId:    utils.GeneratePublicId(),
		SiteID:      siteID,
		TenantID:    tenantID,
		Name:        name,
		Slug:        slug,
		Status:      "draft",
		SoftDeletes: pkgmodels.SoftDeletes{CreatedAt: &now},
	}
}

// SitePageVersion represents a versioned snapshot of a page document.
type SitePageVersion struct {
	Id                    bson.ObjectId        `bson:"_id" json:"id,omitempty"`
	PublicId              string               `bson:"public_id" json:"public_id,omitempty"`
	SiteID                bson.ObjectId        `bson:"site_id,omitempty" json:"site_id,omitempty"`
	SitePageID            bson.ObjectId        `bson:"site_page_id,omitempty" json:"site_page_id,omitempty"`
	TenantID              bson.ObjectId        `bson:"tenant_id,omitempty" json:"tenant_id,omitempty"`
	VersionType           string               `bson:"version_type,omitempty" json:"version_type,omitempty"`
	VersionNumber         int                  `bson:"version_number,omitempty" json:"version_number,omitempty"`
	PuckRoot              map[string]any       `bson:"puck_root,omitempty" json:"puck_root,omitempty"`
	RenderedHTML          string               `bson:"rendered_html,omitempty" json:"rendered_html,omitempty"`
	SEO                   *pkgmodels.SEOConfig `bson:"seo,omitempty" json:"seo,omitempty"`
	Metadata              *SiteVersionMetadata `bson:"metadata,omitempty" json:"metadata,omitempty"`
	CreatedBy             bson.ObjectId        `bson:"created_by,omitempty" json:"created_by,omitempty"`
	pkgmodels.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

// NewSitePageVersion creates a new version snapshot.
func NewSitePageVersion(siteID, pageID, tenantID bson.ObjectId, versionType string, versionNumber int) *SitePageVersion {
	now := time.Now()
	return &SitePageVersion{
		Id:            bson.NewObjectId(),
		PublicId:      utils.GeneratePublicId(),
		SiteID:        siteID,
		SitePageID:    pageID,
		TenantID:      tenantID,
		VersionType:   versionType,
		VersionNumber: versionNumber,
		SoftDeletes:   pkgmodels.SoftDeletes{CreatedAt: &now},
	}
}

// SiteVersionMetadata stores context about how a version was created.
type SiteVersionMetadata struct {
	Prompt          string `bson:"prompt,omitempty" json:"prompt,omitempty"`
	EditInstruction string `bson:"edit_instruction,omitempty" json:"edit_instruction,omitempty"`
	GeneratedBy     string `bson:"generated_by,omitempty" json:"generated_by,omitempty"`
}

// Version type constants.
const (
	VersionTypeDraft     = "draft_snapshot"
	VersionTypePreview   = "preview_snapshot"
	VersionTypePublished = "published_snapshot"
)
