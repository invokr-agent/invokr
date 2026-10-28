package history

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/invokr-agent/invokr/capability/memory/core"
	"github.com/invokr-agent/invokr/capability/memory/sqlite"
	"github.com/invokr-agent/invokr/config"
)

// TestSharedMemoryDBInterleavedAccess covers the one risk introduced by moving
// prompt history into memory.db: a second long-lived *sql.DB handle on the same
// file as the memory store. Every other history test uses its own TempDir, so
// only this one exercises two openers writing and reading in lockstep.
func TestSharedMemoryDBInterleavedAccess(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "memory")

	promptStore, err := NewStore(Config{Dir: dir, Cwd: "/work/alpha", Limit: 1000})
	if err != nil {
		t.Fatalf("history NewStore() error = %v", err)
	}
	defer func() {
		if err := promptStore.Close(); err != nil {
			t.Fatalf("history Close() error = %v", err)
		}
	}()

	memStore, err := sqlite.NewStore(sqlite.Config{Dir: dir})
	if err != nil {
		t.Fatalf("memory NewStore() error = %v", err)
	}
	defer func() {
		if err := memStore.Close(); err != nil {
			t.Fatalf("memory Close() error = %v", err)
		}
	}()

	const rounds = 25
	for i := range rounds {
		prompt := fmt.Sprintf("prompt %d", i)
		if err := promptStore.Append(ctx, prompt); err != nil {
			t.Fatalf("Append(%q) error = %v", prompt, err)
		}
		if _, err := memStore.Save(ctx, core.Record{
			Scope:   config.ScopeProject,
			Kind:    core.KindFact,
			Content: fmt.Sprintf("fact %d", i),
		}); err != nil {
			t.Fatalf("memory Save(%d) error = %v", i, err)
		}

		recent, err := promptStore.Recent(ctx, 1000)
		if err != nil {
			t.Fatalf("Recent() error = %v", err)
		}
		if len(recent) != i+1 {
			t.Fatalf("Recent() len = %d, want %d", len(recent), i+1)
		}
		if recent[len(recent)-1] != prompt {
			t.Fatalf("Recent() newest = %q, want %q", recent[len(recent)-1], prompt)
		}

		records, err := memStore.List(ctx, core.Query{Scopes: []config.MemoryScope{config.ScopeProject}, Limit: 1000})
		if err != nil {
			t.Fatalf("memory List() error = %v", err)
		}
		if len(records) != i+1 {
			t.Fatalf("memory List() len = %d, want %d", len(records), i+1)
		}
	}

	// A memory record whose content matches a prompt must not leak into prompt
	// recall: the two stores share a file, not a table.
	if _, err := memStore.Save(ctx, core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindFact,
		Content: "prompt 0",
	}); err != nil {
		t.Fatalf("memory Save(prompt-lookalike) error = %v", err)
	}
	recent, err := promptStore.Recent(ctx, 1000)
	if err != nil {
		t.Fatalf("Recent() after memory write error = %v", err)
	}
	if len(recent) != rounds {
		t.Fatalf("Recent() len = %d after memory write, want %d", len(recent), rounds)
	}
}
