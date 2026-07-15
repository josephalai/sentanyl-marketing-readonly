package ai

import (
	"fmt"
	"os"
)

// GetConfiguredProvider returns the AI provider configured via environment variables.
func GetConfiguredProvider() (SiteAIProvider, error) {
	provider := os.Getenv("AI_PROVIDER")
	switch provider {
	case "openai":
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY is required when AI_PROVIDER=openai")
		}
		model := os.Getenv("OPENAI_MODEL")
		return NewOpenAIProvider(apiKey, model), nil
	case "gemini":
		apiKey := os.Getenv("GEMINI_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY is required when AI_PROVIDER=gemini")
		}
		model := os.Getenv("GEMINI_MODEL")
		return NewGeminiProvider(apiKey, model), nil
	case "fixture":
		if os.Getenv("SENTANYL_E2E_MODE") != "1" {
			return nil, fmt.Errorf("AI_PROVIDER=fixture is only available in E2E mode")
		}
		return NewFixtureProvider(), nil
	case "":
		// No AI provider configured — AI features will not be available.
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown AI_PROVIDER: %s (supported: openai, gemini, fixture in E2E mode)", provider)
	}
}
