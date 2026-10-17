package agent

import (
	"math"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/turn"

	"go.uber.org/zap"
)

// TestSpendCapPricesCachedPromptTokens is the exact assertion issue #359 asks
// for: with a cached rate configured the session cost is
// uncached×prompt_rate + cached×cached_rate + completion×completion_rate, and
// without one cached tokens fall back to the full prompt rate.
func TestSpendCapPricesCachedPromptTokens(t *testing.T) {
	// 1,000,000 prompt tokens of which 800,000 were served from the cache.
	inFlight := turn.NewSessionTokenUsage(0).AccumulateTurnUsage(turn.TokenUsage{
		Available: true, TurnPromptTokens: 1_000_000, TurnCompletionTokens: 500_000,
		LatestCachedPromptTokens: 800_000,
	})
	// max_session_cost enforcement reads the same figure, mid-request.
	pricing := config.PricingConfig{PromptPer1M: 10, CachedPromptPer1M: 1, CompletionPer1M: 30}
	// 200,000×10/1M + 800,000×1/1M + 500,000×30/1M = 2 + 0.8 + 15 = 17.8
	const want = 17.8
	capped := newSpendCap(config.AgentConfig{MaxSessionCost: want}, pricing, zap.NewNop())
	if reason := capped.exceeded(inFlight); !strings.Contains(reason, "cost budget") {
		t.Fatalf("cost cap reason = %q, want the cached-rate total to trip it", reason)
	}
	if reason := newSpendCap(config.AgentConfig{MaxSessionCost: want + 0.01}, pricing, zap.NewNop()).exceeded(inFlight); reason != "" {
		t.Fatalf("cost cap tripped below the cached-rate total: %q", reason)
	}

	usage := inFlight.RecordUsage()
	if usage.SessionCachedPromptTokens != 800_000 {
		t.Fatalf("SessionCachedPromptTokens = %d, want the provider-reported 800000", usage.SessionCachedPromptTokens)
	}
	priced := newSpendCap(config.AgentConfig{}, pricing, zap.NewNop()).priced(usage)
	if math.Abs(priced.SessionCost-want) > 1e-9 {
		t.Fatalf("SessionCost = %v, want %v", priced.SessionCost, want)
	}

	// Without a cached rate the cached tokens bill at the full prompt rate, so
	// the cost is exactly what a pre-caching config produced: 10 + 15 = 25.
	legacy := config.PricingConfig{PromptPer1M: 10, CompletionPer1M: 30}
	if got := newSpendCap(config.AgentConfig{}, legacy, zap.NewNop()).priced(usage).SessionCost; math.Abs(got-25) > 1e-9 {
		t.Fatalf("unset cached rate priced %v, want the unchanged 25", got)
	}
	// A provider reporting no cached tokens is unchanged either way.
	plain := turn.NewSessionTokenUsage(0).AccumulateTurnUsage(turn.TokenUsage{
		Available: true, TurnPromptTokens: 1_000_000, TurnCompletionTokens: 500_000,
	}).RecordUsage()
	if got := newSpendCap(config.AgentConfig{}, pricing, zap.NewNop()).priced(plain).SessionCost; math.Abs(got-25) > 1e-9 {
		t.Fatalf("uncached session priced %v, want 25", got)
	}
}

// TestPromptCacheRequestExtra covers the opt-in explicit cache markers: off by
// default, a sane openai-compatible default when enabled bare, and verbatim
// pass-through of provider-specific fields.
func TestPromptCacheRequestExtra(t *testing.T) {
	if extra := (config.PromptCacheConfig{}).RequestExtra(); extra != nil {
		t.Fatalf("disabled prompt cache sent %v, want nothing", extra)
	}
	if extra := (config.PromptCacheConfig{RequestFields: map[string]any{"x": 1}}).RequestExtra(); extra != nil {
		t.Fatalf("request fields must stay inert until enabled, got %v", extra)
	}
	if extra := (config.PromptCacheConfig{Enabled: true}).RequestExtra(); extra["cache_prompt"] != true {
		t.Fatalf("enabled prompt cache extra = %v, want cache_prompt", extra)
	}
	custom := config.PromptCacheConfig{Enabled: true, RequestFields: map[string]any{"prompt_cache_key": "sess"}}
	if extra := custom.RequestExtra(); extra["prompt_cache_key"] != "sess" || len(extra) != 1 {
		t.Fatalf("custom prompt cache extra = %v", extra)
	}
}
