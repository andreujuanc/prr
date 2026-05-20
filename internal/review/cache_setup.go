package review

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/andreujuanc/prr/internal/ai"
)

// envDisableCache lets operators turn off provider-side context caching
// for an audit, e.g. for A/B benchmarks comparing cache-on vs cache-off.
const envDisableCache = "PRR_DISABLE_CACHE"

// cacheTTL bounds how long the provider keeps the cache alive. One hour
// is comfortably above any single audit run (the longest deep-review
// runs we've measured top out at ~15 minutes wall-clock at conc=1).
// Past the TTL the provider auto-evicts; the defer-cleanup also runs an
// explicit DELETE on success.
const cacheTTL = 1 * time.Hour

// setupContextCache uploads a per-audit cache prefix to the provider
// and installs the handle on the agent so every subsequent ChatStream
// call carries it. Returns a cleanup func that the caller must defer.
//
// Behaviour matrix:
//   - PRR_DISABLE_CACHE=1                  → skip, return noop cleanup
//   - client is not *ai.Agent              → skip (some callers wrap the agent)
//   - provider does not implement CacheSupport → skip
//   - CreateContextCache returns an error  → log warning, return noop cleanup
//   - success                              → SetCachedContent on agent,
//     return a cleanup func that deletes the cache and logs telemetry
//
// The cache content is currently the canonical tool definitions only.
// System-prompt caching requires splitting the prompt builders into
// static-prefix / variable-suffix and is tracked as a follow-up.
func setupContextCache(ctx context.Context, client ai.Client) func() {
	noop := func() {}

	if os.Getenv(envDisableCache) == "1" {
		log.Printf("review: %s=1, skipping provider cache", envDisableCache)
		return noop
	}

	agent, ok := client.(*ai.Agent)
	if !ok {
		// Some tests pass non-Agent clients; nothing to set the handle on.
		return noop
	}

	cacheSupport, ok := agent.Provider().(ai.CacheSupport)
	if !ok {
		// Provider opted out — Claude Code caches internally, OpenAI
		// caches automatically. No prr-side action needed.
		return noop
	}

	tools := ai.CanonicalToolDefs()
	handle, err := cacheSupport.CreateContextCache(ctx, "", tools, cacheTTL)
	if err != nil {
		// Most common cause on Flash models: tools alone fall below the
		// 1024-token minimum the provider requires. Treat as informational
		// and run uncached — caching is an optimization, not a precondition.
		log.Printf("review: provider cache unavailable, running uncached: %v", err)
		return noop
	}

	agent.SetCachedContent(handle)
	log.Printf("review: provider cache installed (handle=%s)", handle)

	cacheStart := time.Now()
	return func() {
		agent.SetCachedContent("")
		// Use a fresh context for cleanup — the audit's ctx may already
		// be cancelled by the time we get here.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := cacheSupport.DeleteContextCache(cleanupCtx, handle); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("review: cache cleanup failed (non-fatal; TTL will expire): %v", err)
		}
		usage := agent.Usage()
		log.Printf("review: cache stats — duration=%s, cached_input_tokens=%d, total_input_tokens=%d (hit_rate=%s)",
			time.Since(cacheStart).Round(time.Second),
			usage.CacheHits,
			usage.InputTokens,
			cacheHitRateString(usage.CacheHits, usage.InputTokens),
		)
	}
}

func cacheHitRateString(hits, total int) string {
	if total <= 0 {
		return "n/a"
	}
	pct := 100.0 * float64(hits) / float64(total)
	switch {
	case hits == 0:
		return "0% (no cache hits — handle may not be resolving)"
	case pct < 20:
		return fmt.Sprintf("%.0f%% (low — investigate)", pct)
	case pct >= 80:
		return fmt.Sprintf("%.0f%% (good)", pct)
	default:
		return fmt.Sprintf("%.0f%%", pct)
	}
}
