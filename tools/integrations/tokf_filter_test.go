package integrations

import (
	"context"
	"github.com/invokr-ai/invokr/tools/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTokfResultFilterRunsTokfWithMetadataStdinAndEnvironment(t *testing.T) {
	root := t.TempDir()
	argsPath := filepath.Join(root, "args")
	stdinPath := filepath.Join(root, "stdin")
	homePath := filepath.Join(root, "home")
	envPath := filepath.Join(root, "env")
	fakeTokf := filepath.Join(root, "tokf")

	script := `#!/bin/sh
printf '%s\n' "$@" > "$TOKF_TEST_ARGS"
cat > "$TOKF_TEST_STDIN"
printf '%s' "$TOKF_HOME" > "$TOKF_TEST_ENV"
printf 'filtered output'
`
	if err := os.WriteFile(fakeTokf, []byte(script), 0755); err != nil {
		t.Fatalf("write fake tokf: %v", err)
	}

	t.Setenv("TOKF_TEST_ARGS", argsPath)
	t.Setenv("TOKF_TEST_STDIN", stdinPath)
	t.Setenv("TOKF_TEST_ENV", envPath)

	filter := &TokfResultFilter{
		bin:       fakeTokf,
		home:      homePath,
		helperBin: "/tmp/github.com/invokr-ai/invokr",
	}

	got, err := filter.FilterToolResult(context.Background(), core.ToolResultMeta{
		ToolName: "read_file",
		Runtime:  "python",
		Source:   "toolbox",
	}, "raw result")
	if err != nil {
		t.Fatalf("FilterToolResult returned error: %v", err)
	}
	if got != "filtered output" {
		t.Fatalf("expected filtered output, got %q", got)
	}

	argsBytes, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(argsBytes)), "\n")
	wantArgs := []string{"run", "/tmp/github.com/invokr-ai/invokr", "tokf-filter-stdin", "--tool", "read_file", "--source", "toolbox", "--runtime", "python"}
	if strings.Join(args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected tokf args: got %#v want %#v", args, wantArgs)
	}

	stdinBytes, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read captured stdin: %v", err)
	}
	if string(stdinBytes) != "raw result" {
		t.Fatalf("expected raw result on stdin, got %q", string(stdinBytes))
	}

	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read captured env: %v", err)
	}
	if string(envBytes) != homePath {
		t.Fatalf("expected TOKF_HOME %q, got %q", homePath, string(envBytes))
	}
}

func TestTokfResultFilterFallsBackToInputOnTokfFailure(t *testing.T) {
	root := t.TempDir()
	fakeTokf := filepath.Join(root, "tokf")
	if err := os.WriteFile(fakeTokf, []byte("#!/bin/sh\nexit 2\n"), 0755); err != nil {
		t.Fatalf("write fake tokf: %v", err)
	}

	filter := &TokfResultFilter{
		bin:       fakeTokf,
		helperBin: "/tmp/github.com/invokr-ai/invokr",
	}

	got, err := filter.FilterToolResult(context.Background(), core.ToolResultMeta{ToolName: "shell", Source: "toolbox"}, "raw result")
	if err != nil {
		t.Fatalf("FilterToolResult returned error: %v", err)
	}
	if got != "raw result" {
		t.Fatalf("expected unfiltered fallback output, got %q", got)
	}
}
