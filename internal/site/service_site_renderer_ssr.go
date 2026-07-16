package site

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// errPuckRendererDisabled signals that PUCK_RENDERER_URL is unset, so callers
// should fall back to the in-process Go renderer. Not a real failure.
var errPuckRendererDisabled = errors.New("puck-renderer disabled (PUCK_RENDERER_URL unset)")

// puckRenderRequest is the wire body sent to the Node SSR worker's POST /render.
// GlobalStyle's JSON tags already match the worker's EditorDesignTokens shape
// (primary_color, accent_color, …), so we can send the struct as-is.
type puckRenderRequest struct {
	Document    map[string]any        `json:"document"`
	GlobalStyle *pkgmodels.GlobalStyle `json:"globalStyle,omitempty"`
}

type puckRenderResponse struct {
	Body  string `json:"body"`
	Error string `json:"error,omitempty"`
}

// renderViaPuckSSR asks the puck-renderer worker to render the block body of a
// document using the SAME shared registry the editor uses. It returns only the
// <body> markup (self-carrying its block-level token/responsive <style>); the Go
// caller still composes <head>/SEO and the nav/footer chrome around it.
//
// Mirrors the crawlViaSandbox sidecar precedent: env-gated, short timeout, one
// retry, bounded read, and any error is the signal to fall back to the Go
// renderer during bake-in.
func renderViaPuckSSR(doc map[string]any, gs *pkgmodels.GlobalStyle) (string, error) {
	base := os.Getenv("PUCK_RENDERER_URL")
	if base == "" {
		return "", errPuckRendererDisabled
	}
	if doc == nil {
		return "", errors.New("puck-renderer: nil document")
	}

	reqBody, err := json.Marshal(puckRenderRequest{Document: doc, GlobalStyle: gs})
	if err != nil {
		return "", fmt.Errorf("puck-renderer marshal failed: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		body, err := postPuckRender(client, base, reqBody)
		if err == nil {
			return body, nil
		}
		lastErr = err
	}
	return "", lastErr
}

func postPuckRender(client *http.Client, base string, reqBody []byte) (string, error) {
	resp, err := client.Post(base+"/render", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("puck-renderer unreachable: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return "", fmt.Errorf("puck-renderer read failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("puck-renderer returned %d: %s", resp.StatusCode, string(respBytes))
	}

	var pr puckRenderResponse
	if err := json.Unmarshal(respBytes, &pr); err != nil {
		return "", fmt.Errorf("puck-renderer parse failed: %w", err)
	}
	if pr.Body == "" {
		return "", fmt.Errorf("puck-renderer returned empty body (err=%q)", pr.Error)
	}
	return pr.Body, nil
}
