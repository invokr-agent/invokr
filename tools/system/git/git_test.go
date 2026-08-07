package git_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"
)

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("first\nsecond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newCaller(t *testing.T, root string) *tools.Caller {
	t.Helper()
	return tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
		},
	})
}

func call(t *testing.T, caller *tools.Caller, name, args string) (string, error) {
	t.Helper()
	return caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      name,
		Arguments: json.RawMessage(args),
	})
}

func mustCall(t *testing.T, caller *tools.Caller, name, args string) map[string]string {
	t.Helper()
	out, err := call(t, caller, name, args)
	if err != nil {
		t.Fatalf("%s %s: %v", name, args, err)
	}
	var res map[string]string
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	return res
}

func TestGitToolsRegisteredWithPolicies(t *testing.T) {
	caller := newCaller(t, t.TempDir())
	approvals := map[string]string{}
	for _, s := range caller.Specs() {
		approvals[s.Name] = s.Metadata.Policy.Approval
	}
	if approvals["git_read"] != "auto" {
		t.Fatalf("git_read approval = %q, want auto", approvals["git_read"])
	}
	if approvals["git_write"] != "ask" {
		t.Fatalf("git_write approval = %q, want ask", approvals["git_write"])
	}
}

func TestGitReadStatusAndLog(t *testing.T) {
	caller := newCaller(t, newRepo(t))
	status := mustCall(t, caller, "git_read", `{"op":"status"}`)
	if !strings.Contains(status["output"], "a.txt") {
		t.Fatalf("status output = %q, want modified a.txt", status["output"])
	}
	log := mustCall(t, caller, "git_read", `{"op":"log","limit":5}`)
	if !strings.Contains(log["output"], "initial") {
		t.Fatalf("log output = %q, want initial commit", log["output"])
	}
}

func TestGitReadDiffEnvelopeAndPathFilter(t *testing.T) {
	caller := newCaller(t, newRepo(t))
	full := mustCall(t, caller, "git_read", `{"op":"diff"}`)
	if !strings.Contains(full["stat"], "a.txt") || !strings.Contains(full["stat"], "b.txt") {
		t.Fatalf("stat = %q, want both files", full["stat"])
	}
	if !strings.Contains(full["diff"], "+two") || !strings.Contains(full["diff"], "+second") {
		t.Fatalf("diff = %q, want both hunks", full["diff"])
	}
	one := mustCall(t, caller, "git_read", `{"op":"diff","paths":["a.txt"]}`)
	if strings.Contains(one["diff"], "b.txt") {
		t.Fatalf("diff = %q, want b.txt filtered out", one["diff"])
	}
}

func TestGitReadShowDefaultsToHead(t *testing.T) {
	caller := newCaller(t, newRepo(t))
	show := mustCall(t, caller, "git_read", `{"op":"show"}`)
	if !strings.Contains(show["diff"], "initial") || show["stat"] == "" {
		t.Fatalf("show = %#v, want commit header and stat", show)
	}
}

func TestGitReadBadOpFlagsError(t *testing.T) {
	caller := newCaller(t, newRepo(t))
	out, err := call(t, caller, "git_read", `{"op":"blame"}`)
	var toolErr *core.ToolError
	if !json.Valid([]byte(out)) || err == nil || !errors.As(err, &toolErr) || toolErr.Payload == "" {
		t.Fatalf("out=%q err=%v, want flagged ToolError with payload", out, err)
	}
}

func TestGitWriteValidatesArgsBeforeApproval(t *testing.T) {
	caller := newCaller(t, newRepo(t))
	if _, err := call(t, caller, "git_write", `{"op":"commit"}`); err == nil ||
		!strings.Contains(err.Error(), "commit requires message") {
		t.Fatalf("err = %v, want message validation", err)
	}
}

func TestGitWriteRequiresApprover(t *testing.T) {
	caller := newCaller(t, newRepo(t))
	if _, err := call(t, caller, "git_write", `{"op":"add","paths":["a.txt"]}`); err == nil ||
		!strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("err = %v, want approval enforcement without approver", err)
	}
}
