package agent

import (
	"cmp"
	"sync"
	"time"

	"github.com/invokr-ai/invokr/clients"
	"github.com/invokr-ai/invokr/constants"

	"go.uber.org/zap"
)

// ProviderContext keeps the active model and its context limit coherent.
type ProviderContext struct {
	mu                     sync.RWMutex
	fallbackWindow, window int64
	model                  string
	// caps is the /props probe result for the active model; reset on SetModel.
	caps clients.ProviderMetadata
	// logger is optional; nil (the default in tests) simply mutes the
	// minimum-window warning below.
	logger *zap.Logger
	// lowWindowWarned gates the warning below to once per model (reset on
	// SetModel; callers hold c.mu when they flip it).
	lowWindowWarned bool
}

func NewProviderContext(window int64, model ...string) *ProviderContext {
	window = max(0, window)
	c := &ProviderContext{fallbackWindow: window, window: window}
	if len(model) > 0 {
		c.model = model[0]
	}
	c.warnIfPinBelowMinimum()
	return c
}

func (c *ProviderContext) ContextWindowTokens() int64 {
	return readLocked(c, func() int64 { return c.window })
}

// readLocked runs get under the read lock; a nil context yields T's zero value.
func readLocked[T any](c *ProviderContext, get func() T) (value T) {
	if c == nil {
		return value
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return get()
}

// ApplyMetadata records the probe result for model; stale results (another
// model won meanwhile) are ignored.
//
// A configured window is a pin, not a fallback: discovery only fills it in
// when none was configured (0 = unset). A local llama.cpp reporting its build
// limit (131072) used to silently override an INVOKR_CONTEXT_WINDOW_TOKENS of
// 32768, so compaction never fired on the smaller window the operator asked
// for.
//
// The window is min(pin-or-engine, engine): a pin above the engine window is
// clamped to what the engine can give. No summarizer headroom is taken off it
// any more (#577): the summary is a separate, chunked request that carries only
// the old segment, and holding back 4096 tokens capped every run lower than
// pi's. The cost is that the #501 background prefetch finds less room beside
// the current prompt and falls back to a synchronous compaction more often.
//
// Separately, warn once whenever the discovered case (no pin configured)
// lands below constants.MinimumRecommendedContextWindowTokens (#495). A
// configured pin below the floor is warned about independently, from
// warnIfPinBelowMinimum, so it fires even when /props metadata never
// arrives.
func (c *ProviderContext) ApplyMetadata(model string, meta clients.ProviderMetadata) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.model != model {
		return false
	}
	c.caps = meta
	if meta.ContextWindowTokens > 0 {
		pin, engine := c.fallbackWindow, meta.ContextWindowTokens
		base := min(cmp.Or(pin, engine), engine)
		// No summarizer headroom is taken off the window any more: the summary
		// is a separate, chunked request carrying only the old segment, so it
		// never needs room inside this conversation's window (#577).
		c.window = base
		if pin == 0 && c.window < constants.MinimumRecommendedContextWindowTokens {
			c.warnBelowMinimum(model, c.window)
		}
	}
	return meta.ContextWindowTokens > 0
}

// warnIfPinBelowMinimum warns once per model when a configured pin sits below
// the recommended floor, independent of whether /props metadata ever arrives:
// without this, an operator running against a server whose /props never
// resolves saw no warning at all for a too-small pin (#495).
func (c *ProviderContext) warnIfPinBelowMinimum() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fallbackWindow > 0 && c.fallbackWindow < constants.MinimumRecommendedContextWindowTokens {
		c.warnBelowMinimum(c.model, c.fallbackWindow)
	}
}

// warnBelowMinimum logs, once per model, that the effective window is below
// the recommended floor. Called with c.mu held.
func (c *ProviderContext) warnBelowMinimum(model string, window int64) {
	if c.logger == nil || c.lowWindowWarned {
		return
	}
	c.lowWindowWarned = true
	c.logger.Warn("context window below the recommended minimum of 64k tokens",
		zap.String("model", model), zap.Int64("effective_window_tokens", window))
}

// SetLogger attaches the logger used by the warnings above; nil-safe and
// optional so tests that don't care about log output can skip it. It also
// re-checks the pin-below-minimum warning, since a ProviderContext is
// typically constructed before its logger is attached.
func (c *ProviderContext) SetLogger(logger *zap.Logger) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.logger = logger
	c.mu.Unlock()
	c.warnIfPinBelowMinimum()
}

// ReasoningSupport reports the active provider's effort scale and, for
// llama.cpp, whether its chat template honours reasoning_effort and whether it
// honours the enable_thinking switch.
func (c *ProviderContext) ReasoningSupport() (kind clients.ReasoningKind, templateOK, thinkingOK bool) {
	if c == nil {
		return "", false, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.caps.Reasoning, c.caps.ReasoningEffort, c.caps.EnableThinking
}

// AcceptsImages is false only when the server reported the model cannot read images.
func (c *ProviderContext) AcceptsImages() bool {
	return c == nil || readLocked(c, func() bool { return !c.caps.NoVision })
}

func (c *ProviderContext) Model() string { return readLocked(c, func() string { return c.model }) }

// SetModel resets the limit to configured fallback until discovery completes.
func (c *ProviderContext) SetModel(model string) {
	if c == nil || model == "" {
		return
	}
	c.mu.Lock()
	if c.model == model {
		c.mu.Unlock()
		return
	}
	c.model = model
	c.window, c.caps = c.fallbackWindow, clients.ProviderMetadata{}
	c.lowWindowWarned = false
	c.mu.Unlock()
	c.warnIfPinBelowMinimum()
}

func (r *Runner) refreshProviderMetadata(model string) {
	if r == nil || r.provider == nil || r.providerContext == nil || model == "" {
		return
	}
	go r.loadProviderMetadata(model)
}

// metadataProbeAttempts spans roughly a minute of exponential backoff. A
// model-swapping router answers /props before the model is resident, and its
// n_ctx is 0 until then: three attempts inside a second all land on that zero
// and the window stays stuck at the configured fallback for the whole run.
const metadataProbeAttempts = 8

func (r *Runner) loadProviderMetadata(model string) {
	for attempt := range metadataProbeAttempts {
		if r.providerContext.Model() != model {
			return
		}
		metadata, err := r.provider.Metadata(r.backgroundContext, model)
		switch {
		case err == nil && r.providerContext.ApplyMetadata(model, metadata):
			r.logger.Info("provider context window discovered", zap.String("model", model), zap.Int64("context_window_tokens", metadata.ContextWindowTokens), zap.String("source", metadata.Source),
				zap.Int64("window_in_use", r.providerContext.ContextWindowTokens()))
			return
		case err == nil:
			// Answered, but with no window: the model is not loaded yet. Keep
			// waiting rather than settling for the configured fallback.
			r.logger.Debug("provider reports no context window yet", zap.String("model", model), zap.Int("attempt", attempt+1))
		default:
			r.logger.Debug("provider metadata unavailable", zap.String("model", model), zap.Int("attempt", attempt+1), zap.Error(err))
		}
		select {
		case <-r.backgroundContext.Done():
			return
		case <-time.After(time.Duration(1<<attempt) * 250 * time.Millisecond):
		}
	}
}
