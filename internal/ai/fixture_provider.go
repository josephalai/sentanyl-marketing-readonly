package ai

// FixtureProvider is a deterministic site-authoring provider for the
// production-equivalent E2E stack. Embedding the OpenAI implementation keeps
// the full provider contract available, while GenerateSite is overridden so
// certification never depends on a billable or owner-managed credential.
// GetConfiguredProvider makes this provider unreachable outside E2E mode.
type FixtureProvider struct {
	*OpenAIProvider
}

func NewFixtureProvider() *FixtureProvider {
	return &FixtureProvider{OpenAIProvider: NewOpenAIProvider("e2e-fixture", "e2e-fixture")}
}

func (p *FixtureProvider) GenerateSite(req SiteGenerationRequest) (*SiteGenerationResult, error) {
	pages := []struct {
		name, slug, heading, body string
	}{
		{
			name:    "Home",
			slug:    "/",
			heading: "Master the List Method",
			body:    "Joseph Alai teaches a systematic, repeatable manifestation practice grounded in direct experience. Explore the whitepaper library, learn the complete List Method through structured courses, and follow ongoing manifestation research without vague promises or corporate jargon.",
		},
		{
			name:    "Research",
			slug:    "/research",
			heading: "Manifestation Research",
			body:    "Read practical investigations into imaginal acts, persistence, state, and the common denominator behind repeatable results. Each article turns lived experiments into clear exercises students can test, record, and refine for themselves.",
		},
		{
			name:    "Courses",
			slug:    "/courses",
			heading: "Learn the Complete Method",
			body:    "Build a dependable practice with step-by-step video courses, guided exercises, whitepapers, and progress-based lessons. Move from a single well-formed list to consistent application, review, and mastery.",
		},
	}
	count := req.PageCount
	if count <= 0 || count > len(pages) {
		count = len(pages)
	}

	result := &SiteGenerationResult{
		SiteName: "The List Method",
		Theme:    "modern",
		Navigation: &NavigationResult{
			HeaderLinks: []NavLinkResult{{Label: "Home", URL: "/"}, {Label: "Research", URL: "/research"}, {Label: "Courses", URL: "/courses"}},
			FooterLinks: []NavLinkResult{{Label: "Manifestation Research Letter", URL: "/newsletter"}},
		},
		SEO: &SEOResult{MetaTitle: "The List Method", MetaDescription: "Systematic manifestation courses, whitepapers, and research from Joseph Alai."},
	}
	for i, spec := range pages[:count] {
		result.Pages = append(result.Pages, PageGenerationResult{
			Name:   spec.name,
			Slug:   spec.slug,
			IsHome: i == 0,
			SEO:    &SEOResult{MetaTitle: spec.heading, MetaDescription: spec.body},
			PuckRoot: map[string]any{
				"content": []any{
					map[string]any{"type": "HeroSection", "props": map[string]any{"heading": spec.heading, "subheading": "A direct, practical path from intention to consistent application.", "ctaText": "Start learning", "ctaUrl": "/courses"}},
					map[string]any{"type": "RichTextSection", "props": map[string]any{"content": "<h2>A systematic practice</h2><p>" + spec.body + "</p><p>The work is organized so every student can understand the principle, apply it deliberately, and measure progress through experience.</p>"}},
					map[string]any{"type": "CTASection", "props": map[string]any{"heading": "Put the List Method into practice", "description": "Study the complete framework and receive new manifestation research.", "buttonText": "Explore the library", "buttonUrl": "/courses"}},
				},
				"root": map[string]any{"props": map[string]any{}},
			},
		})
	}
	return result, nil
}
