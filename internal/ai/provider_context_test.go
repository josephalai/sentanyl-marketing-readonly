package ai

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type blockingRoundTripper struct{ started chan struct{} }

func (b blockingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	close(b.started)
	<-req.Context().Done()
	return nil, req.Context().Err()
}

// AI-005: durable cancellation is only real if it reaches the provider HTTP
// request. This test intercepts the OpenAI transport and proves the request
// exits promptly when the operation context is canceled.
func TestOpenAIProviderPropagatesCancellation(t *testing.T) {
	original := http.DefaultClient
	started := make(chan struct{})
	http.DefaultClient = &http.Client{Transport: blockingRoundTripper{started: started}}
	t.Cleanup(func() { http.DefaultClient = original })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewOpenAIProvider("test", "gpt-test").GenerateText(GenerateTextRequest{Ctx: ctx, Prompt: "hold"})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("provider request did not abort after cancellation")
	}
}
