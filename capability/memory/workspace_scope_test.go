package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/capability/memory/markdown"
	"github.com/invokr-ai/invokr/config"
)

func saveProjectFact(t *testing.T, memoryDir, root, content string) {
	t.Helper()
	manager, err := NewManagerFromConfigWithEvents(config.AppConfig{
		Memory:    config.MemoryConfig{Dir: memoryDir},
		Workspace: config.WorkspaceConfig{Root: root},
	}, &mockDispatcher{})
	if err != nil {
		t.Fatalf("NewManagerFromConfig() error = %v", err)
	}
	defer func() { _ = manager.Close() }()
	_, err = manager.Save(context.Background(), core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindFact,
		Content: content,
		Source:  core.SourceMarkdown,
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
}

func TestManagerWritesProjectMemoryUnderWorkspaceDigest(t *testing.T) {
	memoryDir := t.TempDir()
	root := filepath.Join(t.TempDir(), "repo")

	saveProjectFact(t, memoryDir, root, "the project builds with make")

	want := filepath.Join(markdown.ProjectDirFor(memoryDir, root), "MEMORY.md")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected project memory at %q: %v", want, err)
	}
	if _, err := os.Stat(filepath.Join(memoryDir, "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatalf("legacy global MEMORY.md must not be written: %v", err)
	}
}

func TestManagerProjectMemoryIsolatedBetweenWorkspaceRoots(t *testing.T) {
	memoryDir := t.TempDir()
	rootA := filepath.Join(t.TempDir(), "a")
	rootB := filepath.Join(t.TempDir(), "b")

	saveProjectFact(t, memoryDir, rootA, "fact for repo a")

	if _, err := os.Stat(filepath.Join(markdown.ProjectDirFor(memoryDir, rootB), "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatalf("repo b must not see repo a's memory: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(markdown.ProjectDirFor(memoryDir, rootA), "MEMORY.md"))
	if err != nil {
		t.Fatalf("read repo a memory: %v", err)
	}
	if string(data) != "- fact for repo a\n" {
		t.Fatalf("unexpected content %q", string(data))
	}
}
