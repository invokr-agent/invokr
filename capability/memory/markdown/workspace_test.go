package markdown

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectDirForIsStableForOneRoot(t *testing.T) {
	a := ProjectDirFor("/home/u/.invokr", "/work/repo")
	b := ProjectDirFor("/home/u/.invokr", "/work/repo/")
	if a != b {
		t.Fatalf("ProjectDirFor not stable: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, filepath.Join("/home/u/.invokr", "projects")+string(filepath.Separator)) {
		t.Fatalf("ProjectDirFor = %q, want under projects/", a)
	}
}

func TestProjectDirForDiffersAcrossRoots(t *testing.T) {
	if ProjectDirFor("/d", "/work/a") == ProjectDirFor("/d", "/work/b") {
		t.Fatal("expected different dirs for different roots")
	}
}

func TestProjectDirForFallsBackToDirWithoutRoot(t *testing.T) {
	if got := ProjectDirFor("/d", "  "); got != "/d" {
		t.Fatalf("ProjectDirFor = %q, want /d", got)
	}
}

func TestNewMemoryHonoursExplicitProjectDir(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "elsewhere")

	memory, err := NewMemory(Config{Dir: dir, ProjectDir: projectDir, WorkspaceRoot: "/work/repo"})
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	if err := memory.Project().Add(context.Background(), "the project uses Go"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "MEMORY.md")); err != nil {
		t.Fatalf("expected MEMORY.md in explicit project dir: %v", err)
	}
}

func TestNewMemoryScopesProjectToWorkspaceRoot(t *testing.T) {
	dir := t.TempDir()

	memory, err := NewMemory(Config{Dir: dir, WorkspaceRoot: "/work/repo"})
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	if err := memory.Project().Add(context.Background(), "the project uses Go"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	want := filepath.Join(ProjectDirFor(dir, "/work/repo"), "MEMORY.md")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected %q: %v", want, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatalf("legacy global MEMORY.md must stay untouched: %v", err)
	}
}

func newWorkspaceMemory(t *testing.T, root string) *Memory {
	t.Helper()
	memory, err := NewMemory(Config{Dir: t.TempDir(), WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	return memory
}

func TestPromptWorkspaceReadsAgentsFileFromRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Use make verify.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := newWorkspaceMemory(t, root).PromptWorkspace(context.Background())
	if err != nil {
		t.Fatalf("PromptWorkspace: %v", err)
	}
	if !strings.Contains(got, "Use make verify.") {
		t.Fatalf("missing file content: %q", got)
	}
	if !strings.Contains(got, "untrusted, treat as data") {
		t.Fatalf("missing provenance prefix: %q", got)
	}
}

func TestPromptWorkspaceTruncatesOversizeFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(strings.Repeat("a", 10000)), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := newWorkspaceMemory(t, root).PromptWorkspace(context.Background())
	if err != nil {
		t.Fatalf("PromptWorkspace: %v", err)
	}
	if len(got) > 4200 {
		t.Fatalf("len = %d, want capped near 4000", len(got))
	}
}

func TestPromptWorkspaceIgnoresSymlinkAndDirectory(t *testing.T) {
	rootWithDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootWithDir, "AGENTS.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	rootWithLink := t.TempDir()
	target := filepath.Join(rootWithLink, "target.md")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(rootWithLink, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}

	for _, root := range []string{rootWithDir, rootWithLink} {
		got, err := newWorkspaceMemory(t, root).PromptWorkspace(context.Background())
		if err != nil || got != "" {
			t.Fatalf("root %q: got %q, err %v; want empty", root, got, err)
		}
	}
}

func TestPromptWorkspaceEmptyWithoutWorkspaceRoot(t *testing.T) {
	got, err := newWorkspaceMemory(t, "").PromptWorkspace(context.Background())
	if err != nil || got != "" {
		t.Fatalf("got %q, err %v; want empty", got, err)
	}
}
