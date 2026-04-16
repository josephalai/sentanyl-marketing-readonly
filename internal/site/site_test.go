package site

import (
	"testing"

	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

func TestValidateSiteCreate(t *testing.T) {
	tests := []struct {
		name    string
		req     SiteCreateRequest
		wantErr bool
	}{
		{
			name:    "valid request",
			req:     SiteCreateRequest{Name: "My Site"},
			wantErr: false,
		},
		{
			name:    "empty name",
			req:     SiteCreateRequest{Name: ""},
			wantErr: true,
		},
		{
			name:    "whitespace name",
			req:     SiteCreateRequest{Name: "   "},
			wantErr: true,
		},
		{
			name:    "name too long",
			req:     SiteCreateRequest{Name: string(make([]byte, MaxSiteNameLength+1))},
			wantErr: true,
		},
		{
			name:    "valid domain",
			req:     SiteCreateRequest{Name: "My Site", Domain: "example.com"},
			wantErr: false,
		},
		{
			name:    "invalid domain",
			req:     SiteCreateRequest{Name: "My Site", Domain: "not valid"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSiteCreate(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSiteCreate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidatePageCreate(t *testing.T) {
	tests := []struct {
		name    string
		req     PageCreateRequest
		wantErr bool
	}{
		{
			name:    "valid request",
			req:     PageCreateRequest{Name: "Home", Slug: "/"},
			wantErr: false,
		},
		{
			name:    "valid slug with path",
			req:     PageCreateRequest{Name: "About", Slug: "/about"},
			wantErr: false,
		},
		{
			name:    "empty name",
			req:     PageCreateRequest{Name: "", Slug: "/about"},
			wantErr: true,
		},
		{
			name:    "empty slug",
			req:     PageCreateRequest{Name: "About", Slug: ""},
			wantErr: true,
		},
		{
			name:    "slug too long",
			req:     PageCreateRequest{Name: "About", Slug: "/" + string(make([]byte, MaxPageSlugLength+1))},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePageCreate(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePageCreate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateSlug(t *testing.T) {
	tests := []struct {
		slug    string
		wantErr bool
	}{
		{"/", false},
		{"/about", false},
		{"/about-us", false},
		{"/blog/post-1", false},
		{"about", false},
		{"UPPER", true},
		{"has space", true},
		{"", true},
	}

	for _, tt := range tests {
		t.Run(tt.slug, func(t *testing.T) {
			err := ValidateSlug(tt.slug)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSlug(%q) error = %v, wantErr %v", tt.slug, err, tt.wantErr)
			}
		})
	}
}

func TestValidateDomain(t *testing.T) {
	tests := []struct {
		domain  string
		wantErr bool
	}{
		{"example.com", false},
		{"sub.example.com", false},
		{"my-site.co.uk", false},
		{"", true},
		{"no-tld", true},
		{"-invalid.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			err := ValidateDomain(tt.domain)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateDomain(%q) error = %v, wantErr %v", tt.domain, err, tt.wantErr)
			}
		})
	}
}

func TestIsKnownComponentType(t *testing.T) {
	if !IsKnownComponentType("HeroSection") {
		t.Error("expected HeroSection to be known")
	}
	if !IsKnownComponentType("SentanylOfferGrid") {
		t.Error("expected SentanylOfferGrid to be known")
	}
	if IsKnownComponentType("NonExistentComponent") {
		t.Error("expected NonExistentComponent to be unknown")
	}
}

func TestGetComponentsByCategory(t *testing.T) {
	groups := GetComponentsByCategory()
	if len(groups) == 0 {
		t.Error("expected non-empty category map")
	}
	// Verify Sentanyl category exists.
	sentanyl, ok := groups[CategorySentanyl]
	if !ok || len(sentanyl) == 0 {
		t.Error("expected Sentanyl category with components")
	}
}

func TestValidatePuckDocument(t *testing.T) {
	tests := []struct {
		name    string
		doc     map[string]any
		wantErr bool
	}{
		{
			name:    "nil document",
			doc:     nil,
			wantErr: true,
		},
		{
			name:    "empty document",
			doc:     map[string]any{},
			wantErr: false,
		},
		{
			name: "valid document with known component",
			doc: map[string]any{
				"content": []any{
					map[string]any{
						"type":  "HeroSection",
						"props": map[string]any{"heading": "Hello"},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "document with unknown component",
			doc: map[string]any{
				"content": []any{
					map[string]any{
						"type":  "UnknownWidget",
						"props": map[string]any{},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "document with missing type",
			doc: map[string]any{
				"content": []any{
					map[string]any{
						"props": map[string]any{},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePuckDocument(tt.doc)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePuckDocument() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseObjectIDs(t *testing.T) {
	// Valid hex IDs
	ids := parseObjectIDs("507f1f77bcf86cd799439011, 507f1f77bcf86cd799439012")
	if len(ids) != 2 {
		t.Errorf("expected 2 IDs, got %d", len(ids))
	}

	// Invalid hex
	ids = parseObjectIDs("not-a-hex, also-bad")
	if len(ids) != 0 {
		t.Errorf("expected 0 IDs, got %d", len(ids))
	}

	// Mixed
	ids = parseObjectIDs("507f1f77bcf86cd799439011, bad, 507f1f77bcf86cd799439012")
	if len(ids) != 2 {
		t.Errorf("expected 2 IDs, got %d", len(ids))
	}

	// Empty
	ids = parseObjectIDs("")
	if len(ids) != 0 {
		t.Errorf("expected 0 IDs, got %d", len(ids))
	}
}

func TestValidateSEO(t *testing.T) {
	tests := []struct {
		name    string
		seo     *pkgmodels.SEOConfig
		wantErr bool
	}{
		{
			name:    "nil seo",
			seo:     nil,
			wantErr: false,
		},
		{
			name:    "valid seo",
			seo:     &pkgmodels.SEOConfig{MetaTitle: "Hello", MetaDescription: "World"},
			wantErr: false,
		},
		{
			name:    "title too long",
			seo:     &pkgmodels.SEOConfig{MetaTitle: string(make([]byte, MaxSEOTitleLength+1))},
			wantErr: true,
		},
		{
			name:    "description too long",
			seo:     &pkgmodels.SEOConfig{MetaDescription: string(make([]byte, MaxSEODescLength+1))},
			wantErr: true,
		},
		{
			name:    "canonical url too long",
			seo:     &pkgmodels.SEOConfig{CanonicalURL: string(make([]byte, MaxSEOURLLength+1))},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSEO(tt.seo)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSEO() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateNavigation(t *testing.T) {
	tests := []struct {
		name    string
		nav     *pkgmodels.NavigationConfig
		wantErr bool
	}{
		{
			name:    "nil navigation",
			nav:     nil,
			wantErr: false,
		},
		{
			name: "valid navigation",
			nav: &pkgmodels.NavigationConfig{
				HeaderNavLinks: []pkgmodels.NavLink{{Label: "Home", URL: "/"}},
				FooterNavLinks: []pkgmodels.NavLink{{Label: "Contact", URL: "/contact"}},
			},
			wantErr: false,
		},
		{
			name: "empty link label",
			nav: &pkgmodels.NavigationConfig{
				HeaderNavLinks: []pkgmodels.NavLink{{Label: "", URL: "/"}},
			},
			wantErr: true,
		},
		{
			name: "label too long",
			nav: &pkgmodels.NavigationConfig{
				HeaderNavLinks: []pkgmodels.NavLink{{Label: string(make([]byte, MaxNavLabelLength+1)), URL: "/"}},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNavigation(tt.nav)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateNavigation() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateSiteUpdate(t *testing.T) {
	tests := []struct {
		name    string
		req     SiteUpdateRequest
		wantErr bool
	}{
		{
			name:    "valid update with name",
			req:     SiteUpdateRequest{Name: "Updated Site"},
			wantErr: false,
		},
		{
			name:    "name too long",
			req:     SiteUpdateRequest{Name: string(make([]byte, MaxSiteNameLength+1))},
			wantErr: true,
		},
		{
			name:    "valid domain update",
			req:     SiteUpdateRequest{Domain: "new.example.com"},
			wantErr: false,
		},
		{
			name:    "invalid domain update",
			req:     SiteUpdateRequest{Domain: "not valid"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSiteUpdate(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSiteUpdate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidatePageUpdate(t *testing.T) {
	tests := []struct {
		name    string
		req     PageUpdateRequest
		wantErr bool
	}{
		{
			name:    "valid update with name",
			req:     PageUpdateRequest{Name: "Updated Page"},
			wantErr: false,
		},
		{
			name:    "name too long",
			req:     PageUpdateRequest{Name: string(make([]byte, MaxPageNameLength+1))},
			wantErr: true,
		},
		{
			name:    "valid slug update",
			req:     PageUpdateRequest{Slug: "/new-path"},
			wantErr: false,
		},
		{
			name:    "invalid slug update",
			req:     PageUpdateRequest{Slug: "HAS SPACES"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePageUpdate(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePageUpdate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidatePuckDocumentDepth(t *testing.T) {
	// Test max component count
	doc := map[string]any{
		"content": make([]any, MaxComponentsPerPage+1),
	}
	for i := range doc["content"].([]any) {
		doc["content"].([]any)[i] = map[string]any{"type": "HeroSection", "props": map[string]any{}}
	}
	if err := ValidatePuckDocument(doc); err == nil {
		t.Error("expected error for too many components")
	}

	// Test invalid content type
	doc = map[string]any{
		"content": "not-an-array",
	}
	if err := ValidatePuckDocument(doc); err == nil {
		t.Error("expected error for non-array content")
	}
}

func TestGetAllComponentDefs(t *testing.T) {
	defs := GetAllComponentDefs()
	if len(defs) == 0 {
		t.Error("expected non-empty component definitions")
	}

	// Check that every component has required fields.
	for _, def := range defs {
		if def.Type == "" {
			t.Error("component definition missing Type")
		}
		if def.Label == "" {
			t.Errorf("component %s missing Label", def.Type)
		}
		if def.Category == "" {
			t.Errorf("component %s missing Category", def.Type)
		}
	}
}

func TestNewSitePage(t *testing.T) {
	page := NewSitePage("Test Page", "/test", "507f1f77bcf86cd799439011", "507f1f77bcf86cd799439012")
	if page.Name != "Test Page" {
		t.Errorf("expected name 'Test Page', got %q", page.Name)
	}
	if page.Slug != "/test" {
		t.Errorf("expected slug '/test', got %q", page.Slug)
	}
	if page.Status != "draft" {
		t.Errorf("expected status 'draft', got %q", page.Status)
	}
	if page.Id == "" {
		t.Error("expected non-empty ID")
	}
	if page.PublicId == "" {
		t.Error("expected non-empty PublicId")
	}
}

func TestNewSitePageVersion(t *testing.T) {
	version := NewSitePageVersion(
		"507f1f77bcf86cd799439011",
		"507f1f77bcf86cd799439012",
		"507f1f77bcf86cd799439013",
		VersionTypeDraft,
		1,
	)
	if version.VersionType != VersionTypeDraft {
		t.Errorf("expected version type %q, got %q", VersionTypeDraft, version.VersionType)
	}
	if version.VersionNumber != 1 {
		t.Errorf("expected version number 1, got %d", version.VersionNumber)
	}
	if version.Id == "" {
		t.Error("expected non-empty ID")
	}
}
