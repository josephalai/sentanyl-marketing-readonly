package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// GeminiProvider implements SiteAIProvider using Google's Gemini API.
type GeminiProvider struct {
	APIKey string
	Model  string
}

// NewGeminiProvider creates a new Gemini provider.
func NewGeminiProvider(apiKey, model string) *GeminiProvider {
	if model == "" {
		model = "gemini-2.5-pro"
	}
	return &GeminiProvider{APIKey: apiKey, Model: model}
}

func (p *GeminiProvider) GenerateSite(req SiteGenerationRequest) (*SiteGenerationResult, error) {
	prompt := buildSiteGenerationPrompt(req)
	fullPrompt := siteGenerationSystemPrompt + "\n\n" + prompt
	resp, err := p.generateContent(fullPrompt)
	if err != nil {
		return nil, err
	}
	var result SiteGenerationResult
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("failed to parse AI response: %w", err)
	}
	return &result, nil
}

func (p *GeminiProvider) GeneratePage(prompt string) (map[string]any, error) {
	fullPrompt := pageGenerationSystemPrompt + "\n\n" + prompt
	resp, err := p.generateContent(fullPrompt)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("failed to parse AI response: %w", err)
	}
	return result, nil
}

func (p *GeminiProvider) EditPage(req PageEditRequest) (*PageEditResult, error) {
	docJSON, _ := json.Marshal(req.CurrentDocument)
	prompt := pageEditSystemPrompt + "\n\nEdit instruction: " + req.Instruction + "\n\nCurrent document:\n" + string(docJSON)
	resp, err := p.generateContent(prompt)
	if err != nil {
		return nil, err
	}
	var result PageEditResult
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("failed to parse AI edit response: %w", err)
	}
	return &result, nil
}

func (p *GeminiProvider) GenerateEmail(req EmailGenerationRequest) (*EmailGenerationResult, error) {
	prompt := buildEmailGenerationPrompt(req.Instruction, req.ContextChunks, req.BrandProfile)
	resp, err := p.generateContent(emailGenerationSystemPrompt + "\n\n" + prompt)
	if err != nil {
		return nil, err
	}
	var result EmailGenerationResult
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("failed to parse AI email response: %w", err)
	}
	return &result, nil
}

func (p *GeminiProvider) EditEmail(req EmailEditRequest) (*EmailGenerationResult, error) {
	prompt := buildEmailEditPrompt(req)
	resp, err := p.generateContent(emailGenerationSystemPrompt + "\n\n" + prompt)
	if err != nil {
		return nil, err
	}
	var result EmailGenerationResult
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("failed to parse AI email edit response: %w", err)
	}
	return &result, nil
}

func (p *GeminiProvider) generateContent(prompt string) (string, error) {
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", p.Model, p.APIKey)

	reqBody := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]string{
					{"text": prompt},
				},
			},
		},
		"generationConfig": map[string]any{
			"responseMimeType": "application/json",
			"temperature":      0.7,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("Gemini API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Gemini API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("failed to parse Gemini response: %w", err)
	}
	if len(apiResp.Candidates) == 0 || len(apiResp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no content in Gemini response")
	}

	return apiResp.Candidates[0].Content.Parts[0].Text, nil
}
