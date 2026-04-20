package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

func (p *OpenAIProvider) GenerateSite(req SiteGenerationRequest) (*SiteGenerationResult, error) {
	prompt := buildSiteGenerationPrompt(req)
	resp, err := p.chatCompletion(prompt, siteGenerationSystemPrompt)
	if err != nil {
		return nil, err
	}
	var result SiteGenerationResult
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("failed to parse AI response: %w", err)
	}
	return &result, nil
}

func (p *OpenAIProvider) GeneratePage(prompt string) (map[string]any, error) {
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
	prompt := fmt.Sprintf("Edit instruction: %s\n\nCurrent document:\n%s", req.Instruction, string(docJSON))
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
