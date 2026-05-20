package review

import (
	"context"
	"testing"
	"time"

	"github.com/andreujuanc/prr/internal/ai"
)

// fakeCacheProvider implements ai.Provider + ai.CacheSupport so the helper
// can exercise the create-cache path without hitting the network.
type fakeCacheProvider struct {
	createCalls int
	deleteCalls int
	failCreate  bool
	lastHandle  string
}

func (p *fakeCacheProvider) Name() string    { return "fake" }
func (p *fakeCacheProvider) ModelID() string { return "fake-model" }
func (p *fakeCacheProvider) Capabilities() ai.Capabilities {
	return ai.Capabilities{PromptCaching: true}
}
func (p *fakeCacheProvider) Chat(context.Context, ai.ChatRequest) (*ai.ChatResponse, error) {
	return nil, nil
}
func (p *fakeCacheProvider) StreamChat(context.Context, ai.ChatRequest) (<-chan ai.ChatEvent, error) {
	return nil, nil
}

func (p *fakeCacheProvider) CreateContextCache(_ context.Context, _ string, _ []ai.ToolDef, _ time.Duration) (string, error) {
	p.createCalls++
	if p.failCreate {
		return "", &fakeErr{msg: "simulated failure"}
	}
	p.lastHandle = "cachedContents/fake-handle"
	return p.lastHandle, nil
}

func (p *fakeCacheProvider) DeleteContextCache(_ context.Context, handle string) error {
	p.deleteCalls++
	if handle != p.lastHandle {
		return &fakeErr{msg: "wrong handle on delete"}
	}
	return nil
}

type fakeErr struct{ msg string }

func (e *fakeErr) Error() string { return e.msg }

// noCacheProvider implements ai.Provider but NOT ai.CacheSupport — used
// to verify the helper takes the no-op path when the provider opts out.
type noCacheProvider struct{}

func (p *noCacheProvider) Name() string                  { return "no-cache" }
func (p *noCacheProvider) ModelID() string               { return "no-cache-model" }
func (p *noCacheProvider) Capabilities() ai.Capabilities { return ai.Capabilities{} }
func (p *noCacheProvider) Chat(context.Context, ai.ChatRequest) (*ai.ChatResponse, error) {
	return nil, nil
}
func (p *noCacheProvider) StreamChat(context.Context, ai.ChatRequest) (<-chan ai.ChatEvent, error) {
	return nil, nil
}

func TestSetupContextCache_HappyPath(t *testing.T) {
	t.Setenv(envDisableCache, "")
	p := &fakeCacheProvider{}
	agent := ai.NewAgent(p, nil)

	cleanup := setupContextCache(context.Background(), agent)
	if p.createCalls != 1 {
		t.Errorf("expected 1 CreateContextCache call, got %d", p.createCalls)
	}
	// Agent should now have a non-empty cache handle. There's no public
	// accessor (by design — read-only after setup), so verify via the
	// known side effect: a subsequent SetCachedContent("") would clear it,
	// which the cleanup does.
	cleanup()
	if p.deleteCalls != 1 {
		t.Errorf("expected 1 DeleteContextCache call, got %d", p.deleteCalls)
	}
}

func TestSetupContextCache_DisabledByEnv(t *testing.T) {
	t.Setenv(envDisableCache, "1")
	p := &fakeCacheProvider{}
	agent := ai.NewAgent(p, nil)

	cleanup := setupContextCache(context.Background(), agent)
	if p.createCalls != 0 {
		t.Errorf("expected 0 CreateContextCache calls when %s=1, got %d", envDisableCache, p.createCalls)
	}
	cleanup() // should not panic and should not call DeleteContextCache
	if p.deleteCalls != 0 {
		t.Errorf("expected 0 DeleteContextCache calls when caching skipped, got %d", p.deleteCalls)
	}
}

func TestSetupContextCache_ProviderOptsOut(t *testing.T) {
	t.Setenv(envDisableCache, "")
	p := &noCacheProvider{}
	agent := ai.NewAgent(p, nil)

	// Should take the no-op path because noCacheProvider does not
	// implement ai.CacheSupport.
	cleanup := setupContextCache(context.Background(), agent)
	cleanup() // must not panic
}

func TestSetupContextCache_CreateFails(t *testing.T) {
	t.Setenv(envDisableCache, "")
	p := &fakeCacheProvider{failCreate: true}
	agent := ai.NewAgent(p, nil)

	cleanup := setupContextCache(context.Background(), agent)
	if p.createCalls != 1 {
		t.Errorf("expected 1 CreateContextCache attempt, got %d", p.createCalls)
	}
	cleanup()
	if p.deleteCalls != 0 {
		t.Errorf("expected 0 DeleteContextCache calls when create failed, got %d", p.deleteCalls)
	}
}

func TestSetupContextCache_NonAgentClient(t *testing.T) {
	t.Setenv(envDisableCache, "")
	// nil client should take the no-op path (type assertion fails).
	// In real code the client would be a non-Agent ai.Client; nil is
	// the simplest stand-in and exercises the same branch.
	cleanup := setupContextCache(context.Background(), nil)
	cleanup() // must not panic
}
