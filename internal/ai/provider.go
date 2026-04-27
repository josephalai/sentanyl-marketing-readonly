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

	// GenerateText resolves a single inline {{ai}} handlebar. Output is
	// plain text (no HTML, no surrounding quotes), capped to MaxTokens or
	// ~80 words. ReferenceText is the concatenated chunks of any context
	// packs the handlebar inherited; the system prompt instructs the model
	// to draw any factual claims or quotes exclusively from it.
	GenerateText(req GenerateTextRequest) (string, error)

	// GenerateNewsletterSeriesOutline plans a multi-issue newsletter series.
	// Mirrors the LMS course-outline call: one LLM hit returns a structured
	// series_title + N issue titles + briefs + key_points anchored to the
	// shared reference material. The caller then fans out per-issue content
	// generation in parallel.
	GenerateNewsletterSeriesOutline(req SeriesOutlineRequest) (*SeriesOutlineResponse, error)

	// GenerateNewsletterPostFromBrief produces one post's Puck doc given the
	// outline-stage brief. Same provider call shape as GeneratePage but
	// pre-loads the issue title, brief, key points, and shared reference
	// text so every post in the series stays grounded in the same source.
	GenerateNewsletterPostFromBrief(req PostFromBriefRequest) (map[string]any, error)
}

// GenerateTextRequest carries the inputs the handlebar resolver passes to
// the provider on a cache miss.
type GenerateTextRequest struct {
	Prompt        string `json:"prompt"`
	ReferenceText string `json:"reference_text,omitempty"` // concatenated context-pack chunks
	BrandProfile  string `json:"brand_profile,omitempty"`
	MaxTokens     int    `json:"max_tokens,omitempty"`
}

// SeriesOutlineRequest is the input for newsletter series outline generation.
// All fields except Topic are advisory; the provider should respect Tone +
// Audience + Outcome but is free to set issue order and key_points.
type SeriesOutlineRequest struct {
	Topic         string `json:"topic"`
	Audience      string `json:"audience,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
	Tone          string `json:"tone,omitempty"`
	IssueCount    int    `json:"issue_count"`
	ReferenceText string `json:"reference_text,omitempty"`
	BrandProfile  string `json:"brand_profile,omitempty"`
}

// SeriesOutlineResponse is the structured plan the provider returns. Each
// IssueOutline becomes a NewsletterPost; the caller paginates them across
// the requested cadence (or drip offsets).
type SeriesOutlineResponse struct {
	SeriesTitle string          `json:"series_title"`
	Description string          `json:"description"`
	Issues      []IssueOutline  `json:"issues"`
}

type IssueOutline struct {
	Order     int      `json:"order"`
	Title     string   `json:"title"`
	Brief     string   `json:"brief"`
	KeyPoints []string `json:"key_points"`
}

// PostFromBriefRequest is the per-issue follow-up generation. Receives one
// IssueOutline plus the same shared grounding as the outline call.
type PostFromBriefRequest struct {
	SeriesTitle   string   `json:"series_title"`
	IssueTitle    string   `json:"issue_title"`
	IssueBrief    string   `json:"issue_brief"`
	KeyPoints     []string `json:"key_points,omitempty"`
	Tone          string   `json:"tone,omitempty"`
	Audience      string   `json:"audience,omitempty"`
	ReferenceText string   `json:"reference_text,omitempty"`
	BrandProfile  string   `json:"brand_profile,omitempty"`
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
