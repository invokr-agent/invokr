package memory

import (
	"context"
	"github.com/invokr-ai/invokr/capability/memory/core"
)

// NoopManager is a Manager that never reads or writes persistent memory.
// It is used for subagent sessions, which should not recall or pollute the
// parent agent's memory directly; the parent records delegated task
// input/output explicitly instead.
type NoopManager struct{}

func NewNoopManager() NoopManager { return NoopManager{} }

func (NoopManager) Close() error { return nil }

func (NoopManager) PromptBlocks(context.Context, core.PromptRequest) (core.PromptBlocks, error) {
	return nil, nil
}

func (NoopManager) Save(_ context.Context, rec core.Record) (core.Record, error) { return rec, nil }

func (NoopManager) Update(_ context.Context, _ string, rec core.Record) (core.Record, error) {
	return rec, nil
}

func (NoopManager) UpdateMetadata(context.Context, string, map[string]string) error { return nil }

func (NoopManager) Delete(context.Context, string) error { return nil }

func (NoopManager) List(context.Context, core.Query) ([]core.Record, error) { return nil, nil }

func (NoopManager) Recall(context.Context, core.Query) ([]core.Hit, error) { return nil, nil }

func (NoopManager) SearchArchive(context.Context, string, int) ([]core.Record, error) {
	return nil, nil
}

func (NoopManager) RestoreArchived(context.Context, []string) ([]string, error) { return nil, nil }
