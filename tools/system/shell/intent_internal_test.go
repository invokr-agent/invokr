package shell

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

func TestShellIntentBecomesTheApprovalSummary(t *testing.T) {
	plan := preparedShellCommand{line: "rm -rf build", tier: shellTierWrite, reasons: []string{"write command"}}
	req, err := shellApprovalRequest(config.ShellConfig{}, plan)
	if err != nil {
		t.Fatalf("shellApprovalRequest returned error: %v", err)
	}
	if req.Summary != "Run rm -rf build" {
		t.Fatalf("summary = %q, want the fallback", req.Summary)
	}
	plan.args.Intent = "clear the stale build output"
	req, err = shellApprovalRequest(config.ShellConfig{}, plan)
	if err != nil {
		t.Fatalf("shellApprovalRequest returned error: %v", err)
	}
	if req.Summary != "clear the stale build output" {
		t.Fatalf("summary = %q, want the stated intent", req.Summary)
	}
	if req.Subject != plan.line {
		t.Fatalf("subject = %q, want the literal command", req.Subject)
	}
}

func TestShellApprovalShowsScriptContents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.sh"), []byte("echo hi\nrm -rf /\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.sh"), []byte(strings.Repeat("echo y\n", 300)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := scriptOf("bash", []string{"x.sh"}, true); got != "x.sh" {
		t.Fatalf("scriptOf(bash x.sh) = %q", got)
	}
	if got := scriptOf("bash", []string{"-c", "ls"}, true); got != "" {
		t.Fatalf("scriptOf(bash -c) = %q, want none", got)
	}
	plan := preparedShellCommand{line: "bash x.sh", tier: shellTierWrite, sandboxExec: true, cwd: dir, scripts: []string{"x.sh", filepath.Join(dir, "big.sh"), "missing.sh"}}
	req, err := shellApprovalRequest(config.ShellConfig{}, plan)
	if err != nil {
		t.Fatalf("shellApprovalRequest returned error: %v", err)
	}
	if !strings.Contains(req.Details, "Script x.sh:\necho hi\nrm -rf /") {
		t.Fatalf("details missing script contents:\n%s", req.Details)
	}
	if strings.Count(req.Details, "echo y") != 200 || !strings.Contains(req.Details, "… truncated") {
		t.Fatalf("big script not capped at 200 lines:\n%s", req.Details)
	}
	if !strings.Contains(req.Details, "Script missing.sh: unreadable") {
		t.Fatalf("missing script not reported:\n%s", req.Details)
	}
}

func TestShellApprovalSkipsScriptWrittenOnTheSameLine(t *testing.T) {
	wsp := workspace.Policy{Root: t.TempDir()}
	if err := os.WriteFile(filepath.Join(wsp.Root, "x.sh"), []byte("echo OLD\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	allowed := []string{"cat"}
	line := "cat > x.sh <<'EOF'\necho NEW\nEOF\nbash x.sh"
	pol, err := classifyLine(line, allowSet(allowed...), allowed, wsp, wsp.Root, true)
	if err != nil {
		t.Fatalf("classifyLine: %v", err)
	}
	plan := preparedShellCommand{line: line, tier: shellTierWrite, sandboxExec: true, cwd: wsp.Root, scripts: pol.scripts, written: pol.written}
	req, err := shellApprovalRequest(config.ShellConfig{}, plan)
	if err != nil {
		t.Fatalf("shellApprovalRequest: %v", err)
	}
	if strings.Contains(req.Details, "OLD") || !strings.Contains(req.Details, "Script x.sh: written by this command line") {
		t.Fatalf("details show stale contents:\n%s", req.Details)
	}
	// A path that is not a regular file is not sandbox-routed; with allowAny it
	// runs on the host and is still shown.
	pol, err = classifyLine("./missing.sh", allowSet(allowed...), allowed, wsp, wsp.Root, true)
	if err != nil || !slices.Equal(pol.scripts, []string{"./missing.sh"}) {
		t.Fatalf("host route scripts = %v, err %v", pol.scripts, err)
	}
	if pol, _ = classifyLine("foo", allowSet(allowed...), allowed, wsp, wsp.Root, true); len(pol.scripts) != 0 {
		t.Fatalf("bare binary recorded as script: %v", pol.scripts)
	}
}
