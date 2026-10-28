package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	memorycore "github.com/invokr-agent/invokr/capability/memory/core"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/runtime"
	"github.com/spf13/cobra"
)

// indexTestMemory is a minimal storing memory.Manager double for the index
// command: Save assigns ids and List honors the doc-kind filter.
type indexTestMemory struct {
	records []memorycore.Record
	nextID  int
}

func (m *indexTestMemory) Close() error { return nil }
func (m *indexTestMemory) PromptBlocks(context.Context, memorycore.PromptRequest) (memorycore.PromptBlocks, error) {
	return nil, nil
}
func (m *indexTestMemory) Save(_ context.Context, rec memorycore.Record) (memorycore.Record, error) {
	m.nextID++
	rec.ID = fmt.Sprintf("doc-%d", m.nextID)
	m.records = append(m.records, rec)
	return rec, nil
}
func (m *indexTestMemory) Update(_ context.Context, id string, rec memorycore.Record) (memorycore.Record, error) {
	return rec, nil
}
func (m *indexTestMemory) UpdateMetadata(context.Context, string, map[string]string) error {
	return nil
}

func (m *indexTestMemory) Delete(context.Context, string) error { return nil }
func (m *indexTestMemory) List(_ context.Context, filter memorycore.Query) ([]memorycore.Record, error) {
	var out []memorycore.Record
	for _, rec := range m.records {
		for _, kind := range filter.Kinds {
			if rec.Kind == kind {
				out = append(out, rec)
			}
		}
	}
	return out, nil
}
func (m *indexTestMemory) Recall(context.Context, memorycore.Query) ([]memorycore.Hit, error) {
	return nil, nil
}
func (m *indexTestMemory) SearchArchive(context.Context, string, int) ([]memorycore.Record, error) {
	return nil, nil
}
func (m *indexTestMemory) RestoreArchived(context.Context, []string) ([]string, error) {
	return nil, nil
}

func TestRootCmdIncludesIndexCommand(t *testing.T) {
	root, _ := NewRootCmd(config.AppConfig{})
	cmd, _, err := root.Find([]string{"index"})
	if err != nil {
		t.Fatalf("find index command: %v", err)
	}
	if cmd == nil || cmd.Name() != "index" {
		t.Fatalf("expected index command, got %#v", cmd)
	}
}

func TestIndexCmdRequiresAllowLargeForLargeWorkspace(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 101; i++ {
		mustWriteIndexTestFile(t, filepath.Join(root, "docs", fmt.Sprintf("file-%03d.txt", i)), fmt.Sprintf("needle-%03d\n", i))
	}
	mem := &indexTestMemory{}
	newCmd := func() *cobra.Command {
		return NewIndexCmd(func() (runtime.Services, error) {
			return runtime.Services{
				App:    config.AppConfig{Workspace: config.WorkspaceConfig{Root: root}},
				Memory: mem,
				Events: events.NewOrderedDispatcher(nil),
			}, nil
		})
	}

	cmd := newCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("expected approval error, got %v", err)
	}
	if len(mem.records) != 0 {
		t.Fatalf("expected no doc records before --allow-large, got %d", len(mem.records))
	}

	cmd = newCmd()
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--allow-large"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("index --allow-large: %v", err)
	}
	if len(mem.records) != 101 {
		t.Fatalf("expected 101 doc records, got %d", len(mem.records))
	}
	if got := out.String(); !strings.Contains(got, "indexed 101 sources at ") {
		t.Fatalf("unexpected index output: %q", got)
	}
}

func mustWriteIndexTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
