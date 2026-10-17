package docker_sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageBuildContextStagesBinaryAndDockerfile(t *testing.T) {
	srcDir := t.TempDir()
	exe := filepath.Join(srcDir, "some-binary")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()
	if err := stageBuildContext(exe, tmpDir); err != nil {
		t.Fatalf("stageBuildContext: %v", err)
	}

	info, err := os.Stat(filepath.Join(tmpDir, "invokr"))
	if err != nil {
		t.Fatalf("staged binary missing: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("staged binary not executable: %v", info.Mode())
	}

	df, err := os.ReadFile(filepath.Join(tmpDir, "Dockerfile"))
	if err != nil {
		t.Fatalf("Dockerfile missing: %v", err)
	}
	content := string(df)
	for _, want := range []string{"COPY invokr /invokr", "RUN chmod +x /invokr", `ENTRYPOINT ["/invokr"]`} {
		if !strings.Contains(content, want) {
			t.Fatalf("Dockerfile missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "github.com/invokr-ai/invokr") {
		t.Fatalf("Dockerfile still references module path:\n%s", content)
	}
}
