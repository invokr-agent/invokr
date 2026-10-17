package read_file

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

func testPolicy(t *testing.T, allowHidden bool) (workspace.Policy, string) {
	t.Helper()
	root := t.TempDir()
	return workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root, AllowHiddenPaths: allowHidden}), root
}

func TestReadFileReturnsVerbatimContent(t *testing.T) {
	wsp, root := testPolicy(t, false)
	content := "line one\nline two\nline three\n"
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := runReadFile("a.txt", readArgs{}, wsp)
	if err != nil {
		t.Fatalf("runReadFile: %v", err)
	}
	if got != content {
		t.Fatalf("content = %q", got)
	}
}

func TestReadFileRejectsTraversalAndSymlinkEscape(t *testing.T) {
	wsp, root := testPolicy(t, false)
	if err := os.Symlink("/etc", filepath.Join(root, "etc-link")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../../../../etc/passwd", "etc-link/passwd"} {
		if _, err := runReadFile(path, readArgs{}, wsp); err == nil {
			t.Fatalf("expected %q to be rejected", path)
		} else if !strings.Contains(err.Error(), "outside allowed workspace roots") {
			t.Fatalf("unexpected error for %q: %v", path, err)
		}
	}
}

func TestReadFileRejectsHiddenPathUnlessAllowed(t *testing.T) {
	wsp, root := testPolicy(t, false)
	if err := os.WriteFile(filepath.Join(root, ".hidden"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runReadFile(".hidden", readArgs{}, wsp); err == nil || !strings.Contains(err.Error(), "hidden") {
		t.Fatalf("expected hidden-path rejection, got %v", err)
	}
	allowed := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root, AllowHiddenPaths: true})
	if got, err := runReadFile(".hidden", readArgs{}, allowed); err != nil || got != "secret" {
		t.Fatalf("allow_hidden_paths read = %q, %v", got, err)
	}
}

func TestReadFileRejectsDirectoryAndBinaryFile(t *testing.T) {
	wsp, root := testPolicy(t, false)
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := runReadFile("sub", readArgs{}, wsp); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected directory rejection, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "img.png"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runReadFile("img.png", readArgs{}, wsp); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("expected binary-extension rejection, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "nul.txt"), []byte("ok\nbad\x00line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runReadFile("nul.txt", readArgs{}, wsp); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("expected mid-file NUL rejection, got %v", err)
	}
}

func TestReadFilePagesWithOffsetAndLimit(t *testing.T) {
	wsp, root := testPolicy(t, false)
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "p.txt"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := runReadFile("p.txt", readArgs{Offset: 3, Limit: 2}, wsp)
	if err != nil {
		t.Fatalf("runReadFile: %v", err)
	}
	want := "line 3\nline 4\n[read_file: lines 3-4 of 10 (truncated); call again with {\"offset\":5}]"
	if got != want {
		t.Fatalf("paged read = %q, want %q", got, want)
	}
}

func TestReadFileTruncationMarkerAbsentOnCompleteRead(t *testing.T) {
	wsp, root := testPolicy(t, false)
	if err := os.WriteFile(filepath.Join(root, "s.txt"), []byte("only line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := runReadFile("s.txt", readArgs{}, wsp)
	if err != nil {
		t.Fatalf("runReadFile: %v", err)
	}
	if strings.Contains(got, "[read_file:") {
		t.Fatalf("complete read carries a marker: %q", got)
	}
}

type captureRegistrar struct {
	specs []core.ToolSpec
}

func (r *captureRegistrar) RegisterNative(spec core.ToolSpec, _ core.NativeHandler) {
	r.specs = append(r.specs, spec)
}
func (r *captureRegistrar) HasNative(string) bool      { return false }
func (r *captureRegistrar) NativeSource(string) string { return "" }
func (r *captureRegistrar) ReplaceNativeToolsBySource(string, []core.DynamicNativeTool) []string {
	return nil
}

func TestReadFileMetadataIsReadOnly(t *testing.T) {
	wsp, _ := testPolicy(t, false)
	registrar := &captureRegistrar{}
	RegisterReadFileTool(registrar, wsp)
	if len(registrar.specs) != 1 {
		t.Fatalf("registered specs = %d, want 1", len(registrar.specs))
	}
	spec := registrar.specs[0]
	if spec.Name != "read" {
		t.Fatalf("name = %q", spec.Name)
	}
	caps := spec.Metadata.Capabilities
	if caps.ExecutesCode || !caps.ReadsWorkspace || caps.WritesWorkspace {
		t.Fatalf("capabilities = %+v, want read-only workspace", caps)
	}
}

// A guessed filename gets the next step, not the host path (#572).
func TestReadFileMissingPathPointsAtFileSearch(t *testing.T) {
	wsp, _ := testPolicy(t, false)
	_, err := runReadFile("internal/render/detail.go", readArgs{}, wsp)
	if err == nil || !strings.Contains(err.Error(), `"internal/render/detail.go" does not exist; run file_search`) || strings.Contains(err.Error(), "/tmp") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadFilesBatchesUnderOneResult(t *testing.T) {
	wsp, root := testPolicy(t, false)
	for name, body := range map[string]string{"a.go": "package a\n", "b.go": "package b\n", "big.txt": strings.Repeat("x", multiReadBytes) + "\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := runReadFiles([]string{"a.go", "missing.go", "b.go", "big.txt"}, wsp)
	for _, want := range []string{"=== a.go ===\npackage a\n", "=== b.go ===\npackage b\n", "=== missing.go ===\nerror: ", "not read (would exceed one result; read them in another call): big.txt"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if len(got) > multiReadBytes+200 {
		t.Fatalf("batch result %d bytes exceeds the budget", len(got))
	}
}
