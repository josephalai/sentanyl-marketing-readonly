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
