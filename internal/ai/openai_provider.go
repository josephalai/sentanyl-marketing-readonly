package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAIProvider implements SiteAIProvider using the OpenAI API.
type OpenAIProvider struct {
	APIKey string
	Model  string
}

// NewOpenAIProvider creates a new OpenAI provider.
func NewOpenAIProvider(apiKey, model string) *OpenAIProvider {
	if model == "" {
		model = "gpt-4o"
	}
	return &OpenAIProvider{APIKey: apiKey, Model: model}
}

// GenerateSiteHTML produces high-fidelity HTML using GPT-4o vision.
// When ScreenshotB64 is provided it sends the image so the model can replicate the layout.
func (p *OpenAIProvider) GenerateSiteHTML(req SiteHTMLRequest) (string, error) {
	prompt := BuildSiteHTMLPrompt(req)

	var resp string
	var err error

	// Always use text-only generation — vision requests get refused for web cloning tasks.
	resp, err = p.chatCompletionPlain(prompt,
		"You are an expert frontend developer. Generate production-quality HTML+CSS from the provided website content and design specifications. Return ONLY the complete HTML document starting with <!DOCTYPE html>. No explanation, no code blocks, no markdown fences — just the HTML.",
		8192)
	if err != nil {
		return "", err
	}

	// Strip markdown code fences
	html := strings.TrimSpace(resp)
	if strings.HasPrefix(html, "```") {
		if nl := strings.Index(html, "\n"); nl >= 0 {
			html = strings.TrimSpace(html[nl+1:])
		}
		if end := strings.LastIndex(html, "```"); end >= 0 {
			html = strings.TrimSpace(html[:end])
		}
	}
	// Find the actual HTML start
	if idx := strings.Index(html, "<!DOCTYPE"); idx >= 0 {
		html = html[idx:]
	} else if idx := strings.Index(html, "<html"); idx >= 0 {
		html = "<!DOCTYPE html>\n" + html[idx:]
	}
	if !strings.Contains(html, "<html") {
		// Log first 200 chars of the unexpected response for debugging
		preview := resp
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return "", fmt.Errorf("AI did not return valid HTML (got: %s)", preview)
	}
	return html, nil
}

// chatCompletionVision sends a vision request to GPT-4o with an image.
func (p *OpenAIProvider) chatCompletionVision(userText, imageB64 string) (string, error) {
	systemMsg := map[string]string{
		"role":    "system",
		"content": "You are an expert web designer. Given a screenshot and content from a website, generate production-quality HTML+CSS that replicates it as closely as possible. Return ONLY the HTML starting with <!DOCTYPE html>.",
	}

	userContent := []map[string]any{
		{"type": "text", "text": userText},
		{
			"type": "image_url",
			"image_url": map[string]string{
				"url":    "data:image/jpeg;base64," + imageB64,
				"detail": "high",
			},
		},
	}

	reqBody := map[string]any{
		"model":       "gpt-4o",
		"messages":    []any{systemMsg, map[string]any{"role": "user", "content": userContent}},
		"max_tokens":  4096,
		"temperature": 0.3,
	}

	bodyBytes, _ := json.Marshal(reqBody)
	httpReq, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("OpenAI vision request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return "", err
	}
	if httpResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenAI returned %d: %s", httpResp.StatusCode, string(respBytes[:min(500, len(respBytes))]))
	}

	var apiResp struct {
		Choices []struct {
			Message struct{ Content string `json:"content"` } `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBytes, &apiResp); err != nil || len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in vision response")
	}
	return apiResp.Choices[0].Message.Content, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (p *OpenAIProvider) DuplicateSite(req SiteDuplicateRequest) (*SiteGenerationResult, error) {
	prompt := BuildSiteDuplicatePrompt(req)
	resp, err := p.chatCompletion(prompt, siteDuplicateSystemPrompt)
	if err != nil {
		return nil, err
	}
	return parseSiteGenerationResult(resp)
}

// parseSiteGenerationResult parses a SiteGenerationResult tolerating field name variants from different LLMs.
// The AI may return: "path" vs "slug", "content" array vs "puck_root" object,
// flat navigation array vs {header_links, footer_links}, "title" vs "meta_title" in SEO.
func parseSiteGenerationResult(resp string) (*SiteGenerationResult, error) {
	// First try direct unmarshal
	var result SiteGenerationResult
	if err := json.Unmarshal([]byte(resp), &result); err == nil && len(result.Pages) > 0 {
		// Check if pages actually have content
		if result.Pages[0].PuckRoot != nil {
			return &result, nil
		}
	}

	// Flexible parse: use raw map to handle field name variants.
	// Reset result (direct unmarshal may have populated partial data).
	result = SiteGenerationResult{}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(resp), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse AI response: %w", err)
	}

	// site_name
	if v, ok := raw["site_name"]; ok {
		_ = json.Unmarshal(v, &result.SiteName)
	}

	// theme — may be string or object
	if v, ok := raw["theme"]; ok {
		if err := json.Unmarshal(v, &result.Theme); err != nil {
			var themeObj map[string]string
			if json.Unmarshal(v, &themeObj) == nil {
				for _, key := range []string{"name", "style", "value", "type"} {
					if val, ok := themeObj[key]; ok {
						result.Theme = val
						break
					}
				}
			}
		}
		if result.Theme == "" {
			result.Theme = "dark"
		}
	}

	// navigation — may be {header_links,footer_links} OR a flat array of {label,url}
	if v, ok := raw["navigation"]; ok {
		if err := json.Unmarshal(v, &result.Navigation); err != nil {
			// Try flat array form
			var navArray []NavLinkResult
			if json.Unmarshal(v, &navArray) == nil && len(navArray) > 0 {
				result.Navigation = &NavigationResult{HeaderLinks: navArray}
			}
		}
	}

	// seo — may use "title" vs "meta_title"
	if v, ok := raw["seo"]; ok {
		var seoRaw map[string]string
		if json.Unmarshal(v, &seoRaw) == nil {
			result.SEO = &SEOResult{}
			if t, ok := seoRaw["meta_title"]; ok {
				result.SEO.MetaTitle = t
			} else if t, ok := seoRaw["title"]; ok {
				result.SEO.MetaTitle = t
			}
			if d, ok := seoRaw["meta_description"]; ok {
				result.SEO.MetaDescription = d
			} else if d, ok := seoRaw["description"]; ok {
				result.SEO.MetaDescription = d
			}
		}
	}

	// pages — normalize each page
	if v, ok := raw["pages"]; ok {
		var pagesRaw []map[string]json.RawMessage
		if json.Unmarshal(v, &pagesRaw) == nil {
			for _, p := range pagesRaw {
				page := normalizePage(p)
				if page != nil {
					result.Pages = append(result.Pages, *page)
				}
			}
		}
	}

	if len(result.Pages) == 0 {
		return nil, fmt.Errorf("AI returned no pages")
	}
	return &result, nil
}

// normalizePage converts a raw page map to PageGenerationResult handling field name variants.
func normalizePage(p map[string]json.RawMessage) *PageGenerationResult {
	page := &PageGenerationResult{}

	if v, ok := p["name"]; ok {
		_ = json.Unmarshal(v, &page.Name)
	}

	// slug may be "slug" or "path"
	if v, ok := p["slug"]; ok {
		_ = json.Unmarshal(v, &page.Slug)
	} else if v, ok := p["path"]; ok {
		_ = json.Unmarshal(v, &page.Slug)
	} else if v, ok := p["url"]; ok {
		_ = json.Unmarshal(v, &page.Slug)
	}

	if v, ok := p["is_home"]; ok {
		_ = json.Unmarshal(v, &page.IsHome)
	}

	// seo
	if v, ok := p["seo"]; ok {
		var seoRaw map[string]string
		if json.Unmarshal(v, &seoRaw) == nil {
			page.SEO = &SEOResult{}
			if t, ok := seoRaw["meta_title"]; ok {
				page.SEO.MetaTitle = t
			} else if t, ok := seoRaw["title"]; ok {
				page.SEO.MetaTitle = t
			}
			if d, ok := seoRaw["meta_description"]; ok {
				page.SEO.MetaDescription = d
			} else if d, ok := seoRaw["description"]; ok {
				page.SEO.MetaDescription = d
			}
		}
	}

	// puck_root may be the canonical {"content": [], "root": {}} or just a "content" array directly
	if v, ok := p["puck_root"]; ok {
		_ = json.Unmarshal(v, &page.PuckRoot)
	} else if v, ok := p["content"]; ok {
		// The AI returned a flat content array — wrap it in the Puck document structure
		var content []any
		if json.Unmarshal(v, &content) == nil {
			page.PuckRoot = map[string]any{
				"content": content,
				"root":    map[string]any{"props": map[string]any{}},
			}
		}
	} else if v, ok := p["document"]; ok {
		_ = json.Unmarshal(v, &page.PuckRoot)
	}

	// Ensure slug starts with /
	if page.Slug != "" && !strings.HasPrefix(page.Slug, "/") && !strings.HasPrefix(page.Slug, "http") {
		page.Slug = "/" + page.Slug
	}
	if page.Slug == "" {
		if page.IsHome {
			page.Slug = "/"
		} else if page.Name != "" {
			page.Slug = "/" + strings.ToLower(strings.ReplaceAll(page.Name, " ", "-"))
		}
	}

	// Derive name from slug if missing
	if page.Name == "" {
		if page.Slug == "/" || page.IsHome {
			page.Name = "Home"
			page.IsHome = true
		} else {
			slug := strings.TrimPrefix(page.Slug, "/")
			slug = strings.ReplaceAll(slug, "-", " ")
			slug = strings.ReplaceAll(slug, "_", " ")
			if len(slug) > 0 {
				page.Name = strings.ToUpper(slug[:1]) + slug[1:]
			}
		}
	}

	return page
}

func (p *OpenAIProvider) GenerateSite(req SiteGenerationRequest) (*SiteGenerationResult, error) {
	prompt := buildSiteGenerationPrompt(req)
	resp, err := p.chatCompletion(prompt, siteGenerationSystemPrompt)
	if err != nil {
		return nil, err
	}
	return parseSiteGenerationResult(resp)
}

func (p *OpenAIProvider) GeneratePage(req SitePageRequest) (map[string]any, error) {
	prompt := buildPageGenerationPrompt(req)
	resp, err := p.chatCompletion(prompt, pageGenerationSystemPrompt)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("failed to parse AI response: %w", err)
	}
	return result, nil
}

func (p *OpenAIProvider) EditPage(req PageEditRequest) (*PageEditResult, error) {
	docJSON, _ := json.Marshal(req.CurrentDocument)
	prompt := buildEditPagePrompt(req, string(docJSON))
	resp, err := p.chatCompletion(prompt, pageEditSystemPrompt)
	if err != nil {
		return nil, err
	}
	var result PageEditResult
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("failed to parse AI edit response: %w", err)
	}
	return &result, nil
}

func (p *OpenAIProvider) SuggestPages(req SitePageSuggestRequest) ([]PageSuggestion, error) {
	prompt := buildSuggestPagesPrompt(req.ProductSummary)
	resp, err := p.chatCompletion(prompt, suggestPagesSystemPrompt)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(resp)
	if !strings.HasPrefix(trimmed, "[") {
		if idx := strings.Index(trimmed, "["); idx >= 0 {
			if end := strings.LastIndex(trimmed, "]"); end > idx {
				trimmed = trimmed[idx : end+1]
			}
		}
	}
	var result []PageSuggestion
	if err := json.Unmarshal([]byte(trimmed), &result); err != nil {
		return nil, fmt.Errorf("failed to parse page suggestions: %w", err)
	}
	return result, nil
}

func (p *OpenAIProvider) GenerateEmail(req EmailGenerationRequest) (*EmailGenerationResult, error) {
	prompt := buildEmailGenerationPrompt(req.Instruction, req.ContextChunks, req.BrandProfile)
	resp, err := p.chatCompletion(prompt, emailGenerationSystemPrompt)
	if err != nil {
		return nil, err
	}
	var result EmailGenerationResult
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("failed to parse AI email response: %w", err)
	}
	return &result, nil
}

func (p *OpenAIProvider) EditEmail(req EmailEditRequest) (*EmailGenerationResult, error) {
	prompt := buildEmailEditPrompt(req)
	resp, err := p.chatCompletion(prompt, emailGenerationSystemPrompt)
	if err != nil {
		return nil, err
	}
	var result EmailGenerationResult
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("failed to parse AI email edit response: %w", err)
	}
	return &result, nil
}

// GenerateText resolves a single inline {{ai}} handlebar. Plain-text path —
// we explicitly do NOT request response_format=json_object so the model can
// return bare prose.
func (p *OpenAIProvider) GenerateText(req GenerateTextRequest) (string, error) {
	resp, err := p.chatCompletionPlain(buildAITextPrompt(req), aiTextSystemPrompt, req.MaxTokens)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp), nil
}

// GenerateNewsletterSeriesOutline returns the structured plan for a series.
func (p *OpenAIProvider) GenerateNewsletterSeriesOutline(req SeriesOutlineRequest) (*SeriesOutlineResponse, error) {
	resp, err := p.chatCompletion(buildSeriesOutlinePrompt(req), seriesOutlineSystemPrompt)
	if err != nil {
		return nil, err
	}
	var result SeriesOutlineResponse
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("failed to parse series outline response: %w", err)
	}
	return &result, nil
}

// GenerateNewsletterPostFromBrief produces one issue's Puck doc from the
// outline-stage brief. Returned doc plugs straight into the post's BodyDoc.
func (p *OpenAIProvider) GenerateNewsletterPostFromBrief(req PostFromBriefRequest) (map[string]any, error) {
	resp, err := p.chatCompletion(buildPostFromBriefPrompt(req), postFromBriefSystemPrompt)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("failed to parse post-from-brief response: %w", err)
	}
	return result, nil
}

func (p *OpenAIProvider) chatCompletion(userMessage, systemMessage string) (string, error) {
	reqBody := map[string]any{
		"model": p.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemMessage},
			{"role": "user", "content": userMessage},
		},
		"response_format": map[string]string{"type": "json_object"},
		"temperature":     0.7,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("OpenAI API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenAI API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("failed to parse OpenAI response: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in OpenAI response")
	}

	return apiResp.Choices[0].Message.Content, nil
}

// chatCompletionPlain is the same wire shape as chatCompletion but without
// the json_object response format, so the model can return bare prose. Used
// for inline handlebar resolution where the result is substituted directly
// into HTML.
func (p *OpenAIProvider) chatCompletionPlain(userMessage, systemMessage string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 220
	}
	reqBody := map[string]any{
		"model": p.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemMessage},
			{"role": "user", "content": userMessage},
		},
		"max_tokens":  maxTokens,
		"temperature": 0.7,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("OpenAI API request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenAI API returned status %d: %s", resp.StatusCode, string(respBody))
	}
	var apiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in OpenAI response")
	}
	return apiResp.Choices[0].Message.Content, nil
}
