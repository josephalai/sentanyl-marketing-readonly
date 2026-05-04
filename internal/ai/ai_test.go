package ai

import (
	"testing"
)

func TestBuildSiteGenerationPrompt(t *testing.T) {
	req := SiteGenerationRequest{
		BusinessName: "Test Corp",
		BusinessType: "consulting",
		Description:  "A consulting firm",
		Theme:        "modern",
		PageCount:    3,
		IncludePages: []string{"Home", "About", "Contact"},
	}

	prompt := buildSiteGenerationPrompt(req)

	if prompt == "" {
		t.Error("expected non-empty prompt")
	}

	// Verify key fields are present in the prompt.
	checks := []string{"Test Corp", "consulting", "A consulting firm", "modern", "3", "Home", "About", "Contact"}
	for _, check := range checks {
		if !contains(prompt, check) {
			t.Errorf("expected prompt to contain %q", check)
		}
	}
}

func TestBuildSiteGenerationPromptDefaults(t *testing.T) {
	req := SiteGenerationRequest{
		BusinessName: "Minimal",
	}

	prompt := buildSiteGenerationPrompt(req)

	if !contains(prompt, "5") {
		t.Error("expected default page count of 5")
	}
}

func TestGetConfiguredProviderEmpty(t *testing.T) {
	// With no env vars set, should return nil, nil.
	provider, err := GetConfiguredProvider()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if provider != nil {
		t.Error("expected nil provider when AI_PROVIDER is unset")
	}
}

func TestNewOpenAIProvider(t *testing.T) {
	p := NewOpenAIProvider("test-key", "")
	if p.Model != "gpt-4o" {
		t.Errorf("expected default model gpt-4o, got %s", p.Model)
	}

	p = NewOpenAIProvider("test-key", "gpt-4")
	if p.Model != "gpt-4" {
		t.Errorf("expected model gpt-4, got %s", p.Model)
	}
}

func TestMergeSimilarConsecutiveSections(t *testing.T) {
	// 3 parallel cards (icon+title+body, no images, no CTAs) → merge.
	in := []ExtractedSection{
		{Heading: "Speed", Body: "Fast everything. Built for scale.", HeadingLevel: 3},
		{Heading: "Security", Body: "SOC 2 compliant out of the box.", HeadingLevel: 3},
		{Heading: "Reliability", Body: "99.99% uptime SLA.", HeadingLevel: 3},
	}
	out := mergeSimilarConsecutiveSections(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 merged section, got %d", len(out))
	}
	if len(out[0].GridItems) != 3 {
		t.Fatalf("expected 3 grid items, got %d", len(out[0].GridItems))
	}

	// Mixed shape: hero + 2 cards → no merge (run too short).
	mixed := []ExtractedSection{
		{Heading: "Welcome", Body: "Long hero body...........................................................", HeadingLevel: 1, ImageURL: "h.jpg"},
		{Heading: "Speed", Body: "Fast.", HeadingLevel: 3},
		{Heading: "Security", Body: "Secure.", HeadingLevel: 3},
	}
	out = mergeSimilarConsecutiveSections(mixed)
	if len(out) != 3 {
		t.Fatalf("expected unchanged length 3, got %d", len(out))
	}

	// Long-headline editorial spotlights: should NOT merge even though they
	// share level + image presence. Joseph Alai's "How It Works" /
	// "THE MOST DEPENDABLE..." / "What Do My Teachings Offer You?" are
	// distinct image-led editorial blocks, not card shapes.
	editorial := []ExtractedSection{
		{Heading: "How It Works", Body: "A short overview.", HeadingLevel: 2, ImageURL: "a.jpg"},
		{Heading: "THE MOST DEPENDABLE AND CONSISTENT WAY TO MANIFEST", Body: "Longer body text describing the system in detail.", HeadingLevel: 2, ImageURL: "b.jpg"},
		{Heading: "What Do My Teachings Offer You?", Body: "Another editorial block.", HeadingLevel: 2, ImageURL: "c.jpg"},
	}
	out = mergeSimilarConsecutiveSections(editorial)
	if len(out) != 3 {
		t.Fatalf("editorial sections should not merge: got %d", len(out))
	}

	// Dark+light boundary: don't merge across tones.
	tones := []ExtractedSection{
		{Heading: "A", HeadingLevel: 3, IsDark: true},
		{Heading: "B", HeadingLevel: 3, IsDark: true},
		{Heading: "C", HeadingLevel: 3, IsDark: false},
	}
	out = mergeSimilarConsecutiveSections(tones)
	if len(out) != 3 {
		t.Fatalf("dark-light boundary: expected unmerged, got %d", len(out))
	}
}

func TestGroupSectionsByBand(t *testing.T) {
	sections := []ExtractedSection{
		{Heading: "Hero", IsDark: true},                          // band 1: inverse
		{Heading: "Feature 1", BgColor: "#f7f7f8"},               // band 2: muted
		{Heading: "Feature 2", BgColor: "#f7f7f8"},               // band 2 cont.
		{Heading: "Quote", BgColor: "#ffffff"},                   // band 3: default
		{Heading: "Final CTA", IsDark: true, BgColor: "#0a0a0a"}, // band 4: inverse
	}
	bands := groupSectionsByBand(sections)
	if got, want := len(bands), 4; got != want {
		t.Fatalf("bands: got %d, want %d", got, want)
	}
	if bands[0].Tone != "inverse" || len(bands[0].Sections) != 1 {
		t.Errorf("band[0]: got tone=%q n=%d, want inverse n=1", bands[0].Tone, len(bands[0].Sections))
	}
	if bands[1].Tone != "muted" || len(bands[1].Sections) != 2 {
		t.Errorf("band[1]: got tone=%q n=%d, want muted n=2", bands[1].Tone, len(bands[1].Sections))
	}
	if bands[2].Tone != "default" || len(bands[2].Sections) != 1 {
		t.Errorf("band[2]: got tone=%q n=%d, want default n=1", bands[2].Tone, len(bands[2].Sections))
	}
	if bands[3].Tone != "inverse" {
		t.Errorf("band[3]: got tone=%q, want inverse", bands[3].Tone)
	}
}

func TestNavLinkToSlugProducesLocalPages(t *testing.T) {
	cases := []struct {
		url, label, want string
	}{
		{"https://mikedillard.com/about-mike-dillard/", "About", "/about"},
		{"https://www.youtube.com/user/Dillard00", "Subscribe", "/subscribe"},
		{"https://example.com/x", "Log in", "/log-in"},
		{"/blog", "Blog", "/blog"},
		{"#", "Pricing", "/pricing"},
		{"", "Contact Us", "/contact-us"},
		{"/", "Home", "/"},
		{"https://twitter.com/x", "", "/x"}, // empty label → URL path
		{"#", "", ""},                       // both empty
	}
	for _, c := range cases {
		if got := navLinkToSlug(c.url, c.label); got != c.want {
			t.Errorf("navLinkToSlug(%q, %q) = %q, want %q", c.url, c.label, got, c.want)
		}
	}
}

func TestParsePageSuggestionsTolerantShapes(t *testing.T) {
	out, err := parsePageSuggestions(`[{"name":"Home","slug":"/","page_type":"home","reason":"r","blocks":["HeroSection"]}]`)
	if err != nil || len(out) != 1 || out[0].Name != "Home" || out[0].PageType != "home" {
		t.Fatalf("canonical: %v %+v", err, out)
	}
	out, err = parsePageSuggestions(`["Home","About","Contact"]`)
	if err != nil || len(out) != 3 || out[1].Name != "About" || out[1].PageType != "about" {
		t.Fatalf("strings: %v %+v", err, out)
	}
	out, err = parsePageSuggestions("```json\n[\"Home\"]\n```")
	if err != nil || len(out) != 1 || out[0].Slug != "/" {
		t.Fatalf("fenced: %v %+v", err, out)
	}
	out, err = parsePageSuggestions(`[{"Name":"Pricing","Slug":"/pricing"}]`)
	if err != nil || len(out) != 1 || out[0].Slug != "/pricing" {
		t.Fatalf("capitalized: %v %+v", err, out)
	}
	if _, err := parsePageSuggestions("not json at all"); err == nil {
		t.Fatal("expected error on garbage input")
	}
}

func TestNewGeminiProvider(t *testing.T) {
	p := NewGeminiProvider("test-key", "")
	if p.Model != "gemini-2.5-pro" {
		t.Errorf("expected default model gemini-2.5-pro, got %s", p.Model)
	}

	p = NewGeminiProvider("test-key", "gemini-1.5-flash")
	if p.Model != "gemini-1.5-flash" {
		t.Errorf("expected model gemini-1.5-flash, got %s", p.Model)
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && s != "" && substr != "" && (len(s) >= len(substr)) && (s == substr || len(s) > len(substr) && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
