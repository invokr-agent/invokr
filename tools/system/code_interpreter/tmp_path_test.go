package code_interpreter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/fsutil"
	"github.com/invokr-agent/invokr/tools/system/workspace"
)

// A /tmp path names the directory the shell tool mounts at /tmp, so a file one
// tool wrote there is the file the other reads.
func TestWorkspaceTmpPathMapsOntoTheSharedScratchDir(t *testing.T) {
	root := "/ws"
	shared := fsutil.SharedTmpDir(root)
	cases := []struct {
		path    string
		want    string
		wantTmp bool
	}{
		{"/tmp/solve.py", filepath.Join(shared, "solve.py"), true},
		{"/tmp", shared, true},
		{"/tmpfile", "/tmpfile", false},
		{"data.csv", "data.csv", false},
	}
	for _, tc := range cases {
		got, isTmp := workspaceTmpPath(tc.path, root)
		if got != tc.want || isTmp != tc.wantTmp {
			t.Fatalf("workspaceTmpPath(%q) = %q,%v; want %q,%v", tc.path, got, isTmp, tc.want, tc.wantTmp)
		}
	}
	if got, isTmp := workspaceTmpPath("/tmp/x", ""); got != "/tmp/x" || isTmp {
		t.Fatalf("with no workspace the path is left alone, got %q,%v", got, isTmp)
	}
}

// The mapped directory is hidden by construction, so the hidden-path rule must
// not reject a /tmp input the model never spelled as a dotfile.
func TestResolveInputsAcceptsSharedTmpPath(t *testing.T) {
	root := t.TempDir()
	dir := fsutil.SharedTmpDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "solve.py"), []byte("print(1)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	inputs, err := resolveInputs([]string{"/tmp/solve.py"}, wsp)
	if err != nil {
		t.Fatalf("resolve /tmp input: %v", err)
	}
	if len(inputs) != 1 || inputs[0].AbsPath != filepath.Join(dir, "solve.py") {
		t.Fatalf("expected the shared tmp file, got %#v", inputs)
	}
}

// Output files map the same way, so code_interpreter can hand a file back
// through /tmp for the shell tool to pick up.
func TestResolveOutputsAcceptsSharedTmpPath(t *testing.T) {
	root := t.TempDir()
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	outputs, err := resolveOutputs([]string{"/tmp/out.txt"}, wsp)
	if err != nil {
		t.Fatalf("resolve /tmp output: %v", err)
	}
	want := filepath.Join(fsutil.SharedTmpDir(root), "out.txt")
	if len(outputs) != 1 || outputs[0].AbsPath != want {
		t.Fatalf("expected %q, got %#v", want, outputs)
	}
	if !strings.HasSuffix(outputs[0].Artifact, "out.txt") {
		t.Fatalf("expected the base name as the artifact, got %#v", outputs[0])
	}
}
