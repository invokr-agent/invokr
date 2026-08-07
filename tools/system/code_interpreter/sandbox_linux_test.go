//go:build linux

package code_interpreter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/tools/core/sandbox"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

// runSandboxedCode runs code through the process backend and returns the
// decoded result, skipping when this host has no working user namespace.
func runSandboxedCode(t *testing.T, code string) Result {
	t.Helper()
	if !sandbox.Available() {
		t.Skip("namespace sandboxing unavailable on this host")
	}
	root := t.TempDir()
	service := NewService(
		config.CodeInterpreterConfig{Backend: constants.CodeInterpreterBackendProcess, SessionDir: filepath.Join(root, ".ci")},
		workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root}),
		nil,
	)
	p, err := service.Prepare(Args{Code: code})
	if err != nil {
		t.Fatal(err)
	}
	out, err := service.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// TestProcessBackendWriteOutsideSessionDirStaysOffHost is issue #298's repro:
// the write itself may land in the sandbox's private tmpfs, but no host file
// may appear.
func TestProcessBackendWriteOutsideSessionDirStaysOffHost(t *testing.T) {
	canary := filepath.Join(t.TempDir(), "artifact-canary-31.txt")
	result := runSandboxedCode(t, fmt.Sprintf("open(%q, 'w').write('artifact-canary-31')\nprint('wrote')", canary))
	if _, err := os.Stat(canary); !os.IsNotExist(err) {
		t.Fatalf("canary escaped to the host at %s (stat err = %v, result = %+v)", canary, err, result)
	}
	if _, err := os.Stat("/tmp/artifact-canary-31.txt"); !os.IsNotExist(err) {
		t.Fatalf("canary escaped to host /tmp: %v", err)
	}
}

func TestProcessBackendArtifactsLandOnlyInSessionArtifactDir(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandboxing unavailable on this host")
	}
	root := t.TempDir()
	sessionRoot := filepath.Join(root, ".ci")
	service := NewService(
		config.CodeInterpreterConfig{Backend: constants.CodeInterpreterBackendProcess, SessionDir: sessionRoot},
		workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root}),
		nil,
	)
	p, err := service.Prepare(Args{Code: "import os\nopen(os.path.join(os.environ['INVOKR_CI_ARTIFACT_DIR'], 'note.txt'), 'w').write('artifact-canary-32')"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := service.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Name != "note.txt" || result.Artifacts[0].Preview != "artifact-canary-32" {
		t.Fatalf("unexpected artifacts: %+v (exit %d, stderr %q)", result.Artifacts, result.ExitCode, result.Stderr)
	}
	data, err := os.ReadFile(filepath.Join(p.artifactDir, "note.txt"))
	if err != nil || string(data) != "artifact-canary-32" {
		t.Fatalf("artifact not under the session artifacts dir: %q, %v", data, err)
	}
	// Nothing outside the session artifacts dir carries the canary.
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		// main.py is the submitted source, which quotes the canary itself.
		if walkErr != nil || entry.IsDir() || entry.Name() == "main.py" || strings.HasPrefix(path, p.artifactDir+string(os.PathSeparator)) {
			return walkErr
		}
		if body, _ := os.ReadFile(path); strings.Contains(string(body), "artifact-canary-32") {
			return fmt.Errorf("canary content found outside the artifacts dir: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProcessBackendCannotReadHostEtcPasswd(t *testing.T) {
	result := runSandboxedCode(t, "print(open('/etc/passwd').readline())")
	if result.ExitCode == 0 && strings.Contains(result.Stdout, "root:") {
		t.Fatalf("host /etc/passwd readable from the sandbox: %+v", result)
	}
}

// TestProcessBackendWorkspaceIsReadOnlyCwd: the interpreter starts in the
// workspace root and reads its files by relative path, but cannot write there.
func TestProcessBackendWorkspaceIsReadOnlyCwd(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandboxing unavailable on this host")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data.csv"), []byte("a,b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := NewService(
		config.CodeInterpreterConfig{Backend: constants.CodeInterpreterBackendProcess, SessionDir: filepath.Join(root, ".ci")},
		workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root}),
		nil,
	)
	code := "import os\nprint(os.getcwd())\nprint(open('data.csv').read().strip())\n" +
		"try:\n    open('canary-33.txt','w').write('x')\n    print('WROTE')\nexcept OSError as e:\n    print('denied')\n"
	p, err := service.Prepare(Args{Code: code})
	if err != nil {
		t.Fatal(err)
	}
	out, err := service.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	want := root + "\na,b\ndenied"
	if strings.TrimSpace(result.Stdout) != want {
		t.Fatalf("stdout = %q, want %q (stderr %q)", result.Stdout, want, result.Stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "canary-33.txt")); !os.IsNotExist(err) {
		t.Fatalf("workspace was writable: %v", err)
	}
}
