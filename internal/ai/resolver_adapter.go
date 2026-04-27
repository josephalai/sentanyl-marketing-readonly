package ai

import "github.com/josephalai/sentanyl/pkg/render"

// resolverAdapter wraps a SiteAIProvider so it satisfies the small
// render.AITextProvider interface. We keep the surfaces separate because
// pkg/render must not depend on marketing-service internals — only on the
// provider trait it actually uses.
type resolverAdapter struct {
	inner SiteAIProvider
}

// NewResolverAdapter exposes a resolver-shaped provider over the
// configured SiteAIProvider. Returns nil when the inner provider is nil so
// the resolver runs in stub-only mode.
func NewResolverAdapter(p SiteAIProvider) render.AITextProvider {
	if p == nil {
		return nil
	}
	return &resolverAdapter{inner: p}
}

func (a *resolverAdapter) GenerateText(req render.AITextRequest) (string, error) {
	return a.inner.GenerateText(GenerateTextRequest{
		Prompt:        req.Prompt,
		ReferenceText: req.ReferenceText,
		BrandProfile:  req.BrandProfile,
		MaxTokens:     req.MaxTokens,
	})
}
