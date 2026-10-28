package code_interpreter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/tools/core/sandbox"
)

func TestOutputCopyCannotFollowEscapingArtifactOrRedirectDestination(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		session, host, output := t.TempDir(), filepath.Join(t.TempDir(), "secret"), filepath.Join(t.TempDir(), "result")
		if err := os.WriteFile(host, []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
		artifacts := filepath.Join(session, "artifacts")
		if err := os.Mkdir(artifacts, 0700); err != nil {
			t.Fatal(err)
		}
		artifact := filepath.Join(artifacts, "result")
		if redirect {
			if err := os.WriteFile(artifact, []byte("overwrite"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(host, output); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Symlink(host, artifact); err != nil {
			t.Fatal(err)
		}
		written, _, _ := copyOutputs(prepared{sessionDir: session, artifactDir: artifacts, outputs: []resolvedOutput{{Requested: "result", Artifact: "result", AbsPath: output}}})
		if len(written) != 0 {
			t.Fatal("host copy followed an escaping symlink")
		}
		if data, err := os.ReadFile(host); err != nil || string(data) != "private" {
			t.Fatalf("host source changed: %q %v", data, err)
		}
		if !redirect {
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("host secret copied into workspace")
			}
		}
	}
}

func TestAutoProcessBackendChecksCreatedProtectedName(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	session := t.TempDir()
	protected := filepath.Join(session, ".invokr")
	p := prepared{args: Args{Code: "import os; os.mkdir('.invokr')"}, cfg: config.CodeInterpreterConfig{PythonBin: python},
		sessionDir: session, inputDir: filepath.Join(session, "inputs"), artifactDir: filepath.Join(session, "artifacts"),
		protected: []string{protected}, timeoutMS: 5000}
	ctx, release := approval.NewPolicy(approval.Auto, false).Begin(context.Background(), approval.Request{Tool: "code_interpreter"})
	defer release()
	result, err := runInProcess(ctx, p)
	if err == nil || !strings.Contains(err.Error(), "removed newly created protected path "+protected) || result.ExitCode == 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := os.Lstat(protected); !os.IsNotExist(err) {
		t.Fatal("created protected name remains")
	}
}
