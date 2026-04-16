package site

import (
	"testing"
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
