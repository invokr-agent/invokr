// Package recallgate implements the optional "nothink" recall gate: a cheap,
// thinking-suppressed probe completion issued at session start that decides
// whether long-term memory recall would help the user's first message and, if
// so, runs targeted recall and renders the hits as a "Relevant Memory" prompt
// block. Off unless wired; every failure mode degrades to "no recall block",
// never a failed turn. See docs/private/plans/nothink-recall-gate.md.
package recallgate

import (
	"cmp"
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/invokr-agent/invokr/capability/memory/core"
	"github.com/invokr-agent/invokr/capability/memory/recall"
	"github.com/invokr-agent/invokr/clients"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/platform/events"
)

// completer is the slice of the chat provider the gate uses for the one-shot
// probe. *clients.Client satisfies it via Summarize.
type completer interface {
	Summarize(ctx context.Context, input clients.SummaryInput) (string, error)
}

// recaller is the slice of the memory manager the gate uses to run recall.
// memory.Manager (TieredManager) satisfies it.
type recaller interface {
	Recall(ctx context.Context, req core.Query) ([]core.Hit, error)
}

// Gate runs the nothink probe and, when it fires, returns a rendered recall
// block. A nil *Gate is a valid "feature off" value: callers must nil-check.
type Gate struct {
	cfg       config.RecallGateConfig
	completer completer
	recaller  recaller
	events    events.Dispatcher
	logger    *zap.Logger
}

// New builds a Gate. The caller only constructs one when the feature is enabled;
// New does not re-check cfg.Enabled.
func New(cfg config.RecallGateConfig, c completer, r recaller, dispatcher events.Dispatcher, logger *zap.Logger) *Gate {
	logger = cmp.Or(logger, zap.NewNop())
	return &Gate{cfg: cfg, completer: c, recaller: r, events: dispatcher, logger: logger}
}

// Block runs the probe for the first user message and, if recall is warranted
// and produces hits, returns the rendered block and true. Every failure mode
// (disabled, short message, probe error/timeout, parse failure, recall error,
// empty hits) returns (zero, false); errors are consumed internally.
func (g *Gate) Block(ctx context.Context, sessionID, userMessage string) (core.PromptBlock, bool) {
	if g == nil || !g.cfg.Enabled {
		return core.PromptBlock{}, false
	}
	if utf8.RuneCountInString(strings.TrimSpace(userMessage)) < g.cfg.MinMessageChars {
		return core.PromptBlock{}, false
	}
	start := time.Now()
	events.Notice(ctx, g.events, sessionID, "", "checking memory…")

	decision, ok := g.probe(ctx, userMessage)
	if !ok || !decision.Recall || len(decision.Queries) == 0 {
		g.logger.Debug("recall gate: no recall", zap.Bool("recall", decision.Recall), zap.Duration("probe", time.Since(start)))
		return core.PromptBlock{}, false
	}

	hits := g.gatherHits(ctx, sessionID, decision.Queries)
	block := recall.HitsPromptBlock(hits, g.cfg.BudgetChars)
	g.logger.Debug("recall gate decision", zap.Int("queries", len(decision.Queries)), zap.Int("hits", len(hits)),
		zap.Duration("probe", time.Since(start)))
	if strings.TrimSpace(block.Content) == "" {
		return core.PromptBlock{}, false
	}
	return block, true
}

// probe issues the one-shot completion and parses it. The bool is false on any
// error (already logged).
func (g *Gate) probe(ctx context.Context, userMessage string) (Decision, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(g.cfg.TimeoutMS)*time.Millisecond)
	defer cancel()

	raw, err := g.completer.Summarize(probeCtx, clients.SummaryInput{
		Prompt: renderPrompt(userMessage, g.cfg.NothinkSuffix), JSON: true,
		Model: g.cfg.Model, MaxTokens: constants.RecallGateMaxTokens})
	if err != nil {
		g.logger.Warn("recall gate: probe failed", zap.Error(err))
		return Decision{}, false
	}
	decision, err := parseDecision(raw, g.cfg, userMessage)
	if err != nil {
		g.logger.Warn("recall gate: probe unparseable", zap.Error(err))
		return Decision{}, false
	}
	return decision, true
}

// gatherHits runs recall for each query and merges the results, deduping by
// recall.RecordKey and capping at MaxHits. Per-query recall errors are logged
// and skipped.
func (g *Gate) gatherHits(ctx context.Context, sessionID string, queries []string) []core.Hit {
	var hits []core.Hit
	seen := make(map[string]struct{})
	for _, query := range queries {
		found, err := g.recaller.Recall(ctx, core.Query{Query: query, SessionID: sessionID, Limit: g.cfg.PerQueryLimit})
		if err != nil {
			g.logger.Warn("recall gate: recall failed", zap.String("query", query), zap.Error(err))
			continue
		}
		for _, hit := range found {
			hits = recall.AppendUniqueHit(max(1, g.cfg.MaxHits), hits, seen, hit)
			if len(hits) >= g.cfg.MaxHits {
				return hits
			}
		}
	}
	return hits
}
