package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The pod has no namespace support. Run a child test with only bash/sleep on
// PATH so sandbox detection takes that same path, even on a sandbox-capable host.
func TestShellWaitDelayPreservesCapturedOutput(t *testing.T) {
	if os.Getenv("INVOKR_TEST_WAIT_DELAY") != "1" {
		bin := t.TempDir()
		for _, name := range []string{"bash", "sleep"} {
			target, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s unavailable: %v", name, err)
			}
			if err := os.Symlink(target, filepath.Join(bin, name)); err != nil {
				t.Fatal(err)
			}
		}
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestShellWaitDelayPreservesCapturedOutput$", "-test.v")
		cmd.Env = append(os.Environ(), "PATH="+bin, "INVOKR_TEST_WAIT_DELAY=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child: %v\n%s", err, out)
		}
		return
	}
	dir := t.TempDir()
	plan := preparedShellCommand{line: "sleep 2 & echo captured-out; echo captured-err >&2", command: "echo", cwd: dir, workspaceRoot: dir, timeoutMS: 5000, maxOutputBytes: 4096}
	out, err := runPreparedShellCommand(nil, context.Background(), plan)
	// The short-lived background process exits naturally; leave none behind.
	time.Sleep(1100 * time.Millisecond)
	if err != nil {
		t.Fatalf("successful shell lost its result: %v; output=%q", err, out)
	}
	result := parseText(out)
	if result.Stdout != "captured-out" || result.Stderr != "captured-err" {
		t.Fatalf("lost output: %+v", result)
	}
	if !result.Truncated || result.TimedOut || result.ExitCode != 0 || !strings.Contains(result.Hint, "tmux") {
		t.Fatalf("missing incomplete-capture status or guidance: %+v", result)
	}
	plan.cwd = filepath.Join(dir, "missing")
	if out, err := runPreparedShellCommand(nil, context.Background(), plan); err == nil || out != "" {
		t.Fatalf("real start failure was swallowed: out=%q err=%v", out, err)
	}
}
