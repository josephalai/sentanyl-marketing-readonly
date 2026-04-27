package ai

import (
	"sync"

	"github.com/josephalai/sentanyl/pkg/render"
)

// Process-wide handlebar resolver. Initialised once at marketing-service
// startup; consumed by broadcast send, public site rendering, and the
// customer post API. Nil-safe — when no LLM provider is configured the
// resolver still returns "[ai unavailable]" placeholders rather than
// panicking.
var (
	resolverMu       sync.RWMutex
	cachedResolver   *render.AIResolver
)

// SetResolver installs the process resolver. Call once at startup.
func SetResolver(r *render.AIResolver) {
	resolverMu.Lock()
	defer resolverMu.Unlock()
	cachedResolver = r
}

// Resolver returns the installed resolver or nil. Callers must tolerate
// nil — the resolver itself is nil-safe but plumbing a *AIResolver around
// is sometimes nicer than a forced no-op singleton.
func Resolver() *render.AIResolver {
	resolverMu.RLock()
	defer resolverMu.RUnlock()
	return cachedResolver
}
