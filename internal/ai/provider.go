package ai

// SiteAIProvider is the interface for AI-powered website generation and editing.
type SiteAIProvider interface {
	// GenerateSite generates a full website structure with pages and content.
	GenerateSite(req SiteGenerationRequest) (*SiteGenerationResult, error)
	// GeneratePage generates a single page's Puck document.
	GeneratePage(prompt string) (map[string]any, error)
	// EditPage returns a set of patch operations to apply to the current document.
	EditPage(req PageEditRequest) (*PageEditResult, error)
	// GenerateEmail generates subject + HTML body for an email.
	GenerateEmail(req EmailGenerationRequest) (*EmailGenerationResult, error)
	// EditEmail rewrites an existing email subject/body given an instruction.
	EditEmail(req EmailEditRequest) (*EmailGenerationResult, error)
}

// EmailGenerationRequest is the input for AI email generation.
type EmailGenerationRequest struct {
	Instruction   string   `json:"instruction"`
	ContextChunks []string `json:"context_chunks,omitempty"` // text from context packs
	BrandProfile  string   `json:"brand_profile,omitempty"`  // brand voice/positioning summary
}

// EmailEditRequest is the input for AI email editing.
type EmailEditRequest struct {
	Instruction    string   `json:"instruction"`
	CurrentSubject string   `json:"current_subject"`
	CurrentBody    string   `json:"current_body"`
	ContextChunks  []string `json:"context_chunks,omitempty"`
	BrandProfile   string   `json:"brand_profile,omitempty"`
}

// EmailGenerationResult is the output of AI email generation.
type EmailGenerationResult struct {
	Subject string `json:"subject"`
	Body    string `json:"body"` // HTML body
	Summary string `json:"summary,omitempty"`
}

// SiteGenerationRequest is the input for generating a full website.
type SiteGenerationRequest struct {
	BusinessName string   `json:"business_name"`
	BusinessType string   `json:"business_type"`
	Description  string   `json:"description"`
	Theme        string   `json:"theme,omitempty"`
	PageCount    int      `json:"page_count,omitempty"`
	IncludePages []string `json:"include_pages,omitempty"`
}

// SiteGenerationResult is the output of AI website generation.
type SiteGenerationResult struct {
	SiteName   string                 `json:"site_name"`
	Theme      string                 `json:"theme"`
	Navigation *NavigationResult      `json:"navigation"`
	SEO        *SEOResult             `json:"seo"`
	Pages      []PageGenerationResult `json:"pages"`
}

// NavigationResult holds generated navigation links.
type NavigationResult struct {
	HeaderLinks []NavLinkResult `json:"header_links"`
	FooterLinks []NavLinkResult `json:"footer_links"`
}

// NavLinkResult is a generated nav link.
type NavLinkResult struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// SEOResult holds generated SEO config.
type SEOResult struct {
	MetaTitle       string `json:"meta_title"`
	MetaDescription string `json:"meta_description"`
}

// PageGenerationResult holds a generated page.
type PageGenerationResult struct {
	Name     string         `json:"name"`
	Slug     string         `json:"slug"`
	IsHome   bool           `json:"is_home"`
	SEO      *SEOResult     `json:"seo"`
	PuckRoot map[string]any `json:"puck_root"`
}

// PageEditRequest is the input for AI page editing.
type PageEditRequest struct {
	Instruction     string         `json:"instruction"`
	CurrentDocument map[string]any `json:"current_document"`
}

// PageEditResult is the output of AI page editing — returns the full modified document.
type PageEditResult struct {
	Document map[string]any `json:"document"`
	Summary  string         `json:"summary"`
}
