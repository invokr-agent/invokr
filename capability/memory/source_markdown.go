package memory

import (
	"context"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/capability/memory/markdown"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
)

// pinnedMarkdownSource adapts the pinned markdown documents (agents/user/project)
// into the recall.Source and record-listing shapes the manager needs.
type pinnedMarkdownSource struct {
	markdown *markdown.Memory
}

func (s pinnedMarkdownSource) PromptBlocks(ctx context.Context, req core.PromptRequest) (core.PromptBlocks, error) {
	candidates := []struct {
		title    string
		priority int
		render   func(context.Context) (string, error)
	}{
		{constants.UserMemoryPromptTitle, constants.UserMemoryPromptPriority, s.markdown.PromptUser},
		{constants.WorkspaceInstructionsPromptTitle, constants.WorkspaceInstructionsPromptPriority, s.markdown.PromptWorkspace},
		{constants.ProjectMemoryPromptTitle, constants.ProjectMemoryPromptPriority, s.markdown.PromptProject},
	}

	blocks := make([]core.PromptBlock, 0, len(candidates))
	for _, candidate := range candidates {
		content, err := candidate.render(ctx)
		if err != nil {
			return nil, err
		}
		if content = strings.TrimSpace(content); content == "" {
			continue
		}
		blocks = append(blocks, core.PromptBlock{
			Title: candidate.title, Content: content, Priority: candidate.priority, Source: core.SourceMarkdown})
	}
	return blocks, nil
}

func (s pinnedMarkdownSource) Records(ctx context.Context, filter core.Query) ([]core.Record, error) {
	var records []core.Record
	if len(filter.Kinds) > 0 && !slices.Contains(filter.Kinds, core.KindFact) {
		return records, nil
	}
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	for _, scope := range scopesForFilter(filter) {
		scopeRecords, err := s.recordsForScope(ctx, scope, query, filter.Limit-len(records))
		if err != nil {
			return nil, err
		}
		records = append(records, scopeRecords...)
		if filter.Limit > 0 && len(records) >= filter.Limit {
			return records, nil
		}
	}
	return records, nil
}

func (s pinnedMarkdownSource) recordsForScope(ctx context.Context, scope config.MemoryScope, query string, limit int) ([]core.Record, error) {
	doc, err := s.markdown.Document(scope)
	if err != nil {
		return nil, err
	}
	rows, err := doc.List(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]core.Record, 0, len(rows))
	for _, row := range rows {
		if query != "" && !strings.Contains(strings.ToLower(row), query) {
			continue
		}
		records = append(records, core.Record{ID: markdown.EntryID(row), Scope: scope, Kind: core.KindFact, Content: row, Source: core.SourceMarkdown})
		if limit > 0 && len(records) >= limit {
			break
		}
	}
	return records, nil
}
