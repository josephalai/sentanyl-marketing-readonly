package routes

import (
	"context"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/ai"
	"github.com/josephalai/sentanyl/pkg/aigov"
)

// governedGenerateText covers background inbox-agent provider calls that do
// not pass through the synchronous authoring handlers. They share the same
// tenant budget, concurrency projection, durable cancellation, and crash
// recovery as interactive AI operations.
func governedGenerateText(parent context.Context, tenantID bson.ObjectId, surface string, provider ai.SiteAIProvider, req ai.GenerateTextRequest) (string, error) {
	inputChars := int64(len(req.Prompt) + len(req.ReferenceText) + len(req.BrandProfile))
	op, err := aigov.Begin(tenantID, surface, aigov.Estimate{InputCharacters: inputChars, OutputTokens: int64(req.MaxTokens)}, time.Now().UTC())
	if err != nil {
		return "", err
	}
	ctx, cancel := aigov.Context(parent, op)
	defer cancel()
	req.Ctx = ctx
	text, err := provider.GenerateText(req)
	if err != nil {
		_ = aigov.Fail(op, err, time.Now().UTC())
		return "", err
	}
	_ = aigov.Complete(op, aigov.Usage{}, time.Now().UTC())
	return text, nil
}
