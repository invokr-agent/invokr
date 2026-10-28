package memory

import (
	"cmp"
	"context"
	"fmt"

	"github.com/invokr-agent/invokr/capability/memory/summary"
	"github.com/invokr-agent/invokr/clients"
)

// ChatSummarizer is the slice of the main chat provider the memory manager uses
// to summarize transcripts. *clients.Client satisfies it.
type ChatSummarizer interface {
	Summarize(ctx context.Context, input clients.SummaryInput) (string, error)
}

// ModelSource exposes the active provider model to background summarization.
type ModelSource interface {
	Model() string
}

// memorySummarizer is the manager-internal summarization role. Both the zerfoo
// worker and chatSummarizer satisfy it.
type memorySummarizer interface {
	Summarize(context.Context, summary.StructuredSummaryInput) (summary.MemorySummary, error)
}

// chatSummarizer routes memory summarization to the main model's chat endpoint
// instead of zerfoo's in-process generator. zerfoo's CPU decode is ~17s/token
// (a single summary hung >10min), so in-process generation is non-viable;
// embeddings (single forward pass) stay on zerfoo.
type chatSummarizer struct {
	client ChatSummarizer
	model  string
	models ModelSource
}

func (s chatSummarizer) Summarize(ctx context.Context, input summary.StructuredSummaryInput) (summary.MemorySummary, error) {
	prompt, err := summary.RenderSummaryPrompt(input)
	if err != nil {
		return summary.MemorySummary{}, err
	}
	raw, err := s.client.Summarize(ctx, clients.SummaryInput{Prompt: prompt, JSON: true, Model: s.activeModel()})
	if err != nil {
		return summary.MemorySummary{}, err
	}
	return summary.ParseMemorySummary(raw)
}

func (s chatSummarizer) activeModel() string {
	if s.models == nil {
		return s.model
	}
	return cmp.Or(s.models.Model(), s.model)
}

// titleMaxTokens caps the title completion. Generous enough that a model which
// ignores the no-think suffix still has room to emit the title after reasoning.
const titleMaxTokens = 2048

// Title generates a one-line label for a transcript. It is plain text, not the
// memory-extraction schema, so it stays separate from Summarize. The curator
// reaches it by type assertion, keeping it off the memorySummarizer interface
// that the zerfoo worker also satisfies.
func (s chatSummarizer) Title(ctx context.Context, lines []string) (string, error) {
	prompt, err := summary.RenderTitlePrompt(lines)
	if err != nil {
		return "", err
	}
	raw, err := s.client.Summarize(ctx, clients.SummaryInput{
		Prompt: prompt, Model: s.activeModel(), MaxTokens: titleMaxTokens})
	if err != nil {
		return "", err
	}
	title := summary.CleanTitle(raw)
	if title == "" {
		return "", fmt.Errorf("titling produced an empty result")
	}
	return title, nil
}
