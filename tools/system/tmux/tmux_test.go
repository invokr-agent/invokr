package tmux_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/support/testutil"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/tmux"
)

func TestTmuxOpenListKill(t *testing.T) {
	caller := newCaller(t)
	if !hasSpec(caller.Specs(), "tmux") {
		t.Fatal("tmux spec not registered")
	}
	opened := call(t, caller, `{"action":"open","command":"cat"}`)
	var result map[string]string
	if err := json.Unmarshal([]byte(opened), &result); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result["session"], "m-") {
		t.Fatalf("unexpected session: %q", result["session"])
	}
	if listed := call(t, caller, `{"action":"list"}`); !strings.Contains(listed, result["session"]) {
		t.Fatalf("session missing from list: %q", listed)
	}
	if got := call(t, caller, `{"action":"kill","session":"`+result["session"]+`"}`); got != "killed" {
		t.Fatalf("kill returned %q", got)
	}
	if listed := call(t, caller, `{"action":"list"}`); strings.Contains(listed, result["session"]) {
		t.Fatalf("session remains: %q", listed)
	}
}

func TestTmuxRejectsInvalidOpen(t *testing.T) {
	caller := newCaller(t)
	for _, args := range []string{`{"action":"open"}`, `{"action":"bogus"}`} {
		if _, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "tmux", Arguments: json.RawMessage(args)}); err == nil {
			t.Fatalf("expected error for %s", args)
		}
	}
}

func TestTmuxSendAndRead(t *testing.T) {
	caller := newCaller(t)
	session := openSession(t, caller, "cat")
	call(t, caller, `{"action":"send","session":"`+session+`","keys":["hello","Enter"]}`)
	if screen := call(t, caller, `{"action":"read","session":"`+session+`","wait_ms":300}`); !strings.Contains(screen, "hello") {
		t.Fatalf("screen missing sent text: %q", screen)
	}
	call(t, caller, `{"action":"send","session":"`+session+`","keys":["Enter"],"literal":true}`)
	if screen := call(t, caller, `{"action":"read","session":"`+session+`","wait_ms":100}`); !strings.Contains(screen, "Enter") {
		t.Fatalf("screen missing literal text: %q", screen)
	}
}

func TestTmuxSendRejectsEmptyKeys(t *testing.T) {
	caller := newCaller(t)
	session := openSession(t, caller, "cat")
	for _, args := range []string{
		`{"action":"send","session":"` + session + `"}`,
		`{"action":"send","session":"` + session + `","keys":[]}`,
		`{"action":"send","session":"` + session + `","keys":[""]}`,
	} {
		_, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "tmux", Arguments: json.RawMessage(args)})
		if err == nil || !strings.Contains(err.Error(), "keys are required for send") {
			t.Fatalf("expected empty send rejection for %s, got %v", args, err)
		}
	}
}

func TestTmuxSendToMissingSessionReturnsToolError(t *testing.T) {
	caller := newCaller(t)
	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name: "tmux", Arguments: json.RawMessage(`{"action":"send","session":"missing","keys":["x"]}`),
	})
	var toolErr *core.ToolError
	if !errors.As(err, &toolErr) || toolErr.Payload == "" {
		t.Fatalf("expected ToolError with payload, got %v", err)
	}
}

func TestTmuxOpenRequiresApproval(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	root := t.TempDir()
	caller := tools.NewCaller(core.CallerDeps{App: config.AppConfig{
		Toolbox: config.ToolboxConfig{Dir: t.TempDir()}, Workspace: config.WorkspaceConfig{Root: root},
	}})
	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name: "tmux", Arguments: json.RawMessage(`{"action":"open","command":"cat"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("expected approval error, got %v", err)
	}
}

func TestTmuxRejectsCWDOutsideWorkspace(t *testing.T) {
	caller := newCaller(t)
	outside := t.TempDir()
	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name: "tmux", Arguments: json.RawMessage(`{"action":"open","command":"cat","cwd":"` + outside + `"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "outside allowed workspace roots") {
		t.Fatalf("expected workspace rejection, got %v", err)
	}
}

func newCaller(t *testing.T) *tools.Caller {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	root := t.TempDir()
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", tmux.SocketPath(root), "kill-server").Run()
	})
	return tools.NewCaller(core.CallerDeps{App: config.AppConfig{
		Toolbox: config.ToolboxConfig{Dir: t.TempDir()}, Workspace: config.WorkspaceConfig{Root: root},
	}, Approver: testutil.StaticApprover{Decision: approval.Decision{Approved: true}}})
}

func call(t *testing.T, caller *tools.Caller, args string) string {
	t.Helper()
	out, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "tmux", Arguments: json.RawMessage(args)})
	if err != nil {
		var toolErr *core.ToolError
		if errors.As(err, &toolErr) {
			t.Fatalf("%v: %s", err, toolErr.Payload)
		}
		t.Fatal(err)
	}
	return out
}

func hasSpec(specs []core.ToolSpec, name string) bool {
	for _, spec := range specs {
		if spec.Name == name {
			return true
		}
	}
	return false
}

func TestTmuxShortLivedCommandStaysReadable(t *testing.T) {
	caller := newCaller(t)
	opened := call(t, caller, `{"action":"open","command":"echo invokr-was-here"}`)
	var result map[string]string
	if err := json.Unmarshal([]byte(opened), &result); err != nil {
		t.Fatal(err)
	}
	session := result["session"]
	if screen := call(t, caller, `{"action":"read","session":"`+session+`","wait_ms":300}`); !strings.Contains(screen, "invokr-was-here") {
		t.Fatalf("expected output of exited command on screen, got %q", screen)
	}
	if listed := call(t, caller, `{"action":"list"}`); !strings.Contains(listed, "dead=1") {
		t.Fatalf("expected list to flag the dead pane, got %q", listed)
	}
	if got := call(t, caller, `{"action":"kill","session":"`+session+`"}`); got != "killed" {
		t.Fatalf("kill returned %q", got)
	}
}

func TestTmuxSocketNameIsPerProcessUnique(t *testing.T) {
	name := strings.TrimSuffix(filepath.Base(tmux.SocketPath("")), ".sock")
	if !regexp.MustCompile(fmt.Sprintf(`^invokr-%d-[0-9a-f]{4}$`, os.Getpid())).MatchString(name) {
		t.Fatalf("unexpected socket name %q", name)
	}
}

func TestTmuxSendLeadingDashKeyIsLiteral(t *testing.T) {
	caller := newCaller(t)
	session := openSession(t, caller, "cat")
	call(t, caller, `{"action":"send","session":"`+session+`","keys":["-l"],"literal":true}`)
	if screen := call(t, caller, `{"action":"read","session":"`+session+`","wait_ms":300}`); !strings.Contains(screen, "-l") {
		t.Fatalf("expected literal -l on screen, got %q", screen)
	}
}

// The server must outlive the model's last visible session, and must not be
// matched by a pkill aimed at a pane's own command: the server used to inherit
// the starting client's argv, so `pkill -f '<command>'` killed it (#528). The
// pkill is typed into a second session, since the first pane is the sleep.
func TestTmuxServerSurvivesPaneKill(t *testing.T) {
	caller := newCaller(t)
	sleeper := openSession(t, caller, "sleep 9871") // starts the server
	shell := openSession(t, caller, "sh")
	call(t, caller, `{"action":"send","session":"`+shell+`","keys":["pkill -f 'sleep 9871'","Enter"]}`)
	call(t, caller, `{"action":"read","session":"`+shell+`","wait_ms":500}`)
	if listed := call(t, caller, `{"action":"list"}`); !strings.Contains(listed, sleeper) {
		t.Fatalf("server died with the pane: %q", listed)
	}
	for _, session := range []string{sleeper, shell} {
		if got := call(t, caller, `{"action":"kill","session":"`+session+`"}`); got != "killed" {
			t.Fatalf("kill returned %q", got)
		}
	}
	// The hidden holder keeps the server up once the last visible session goes.
	if listed := call(t, caller, `{"action":"list"}`); listed != "no sessions" {
		t.Fatalf("expected the holder to be hidden, got %q", listed)
	}
	if _, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name: "tmux", Arguments: json.RawMessage(`{"action":"send","session":"_invokr_holder","keys":["Enter"]}`),
	}); err != nil {
		t.Fatalf("server did not survive the kill: %v", err)
	}
}

func openSession(t *testing.T, caller *tools.Caller, command string) string {
	t.Helper()
	var result map[string]string
	if err := json.Unmarshal([]byte(call(t, caller, `{"action":"open","command":"`+command+`"}`)), &result); err != nil {
		t.Fatal(err)
	}
	return result["session"]
}

func TestTmuxHostServerReachesUserSessions(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", "userbot", "cat").CombinedOutput(); err != nil {
		t.Fatalf("start user server: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-server").Run() })
	caller := tools.NewCaller(core.CallerDeps{App: config.AppConfig{
		Toolbox: config.ToolboxConfig{Dir: t.TempDir()}, Workspace: config.WorkspaceConfig{Root: t.TempDir()},
		Tools: config.ToolsConfig{TmuxHostServer: true},
	}, Approver: testutil.StaticApprover{Decision: approval.Decision{Approved: true}}})
	// Registration sets the process-wide host flag; a default caller resets it.
	t.Cleanup(func() {
		tools.NewCaller(core.CallerDeps{App: config.AppConfig{Toolbox: config.ToolboxConfig{Dir: t.TempDir()}}})
	})
	if listed := call(t, caller, `{"action":"list"}`); !strings.Contains(listed, "userbot") {
		t.Fatalf("user session missing from host list: %q", listed)
	}
	call(t, caller, `{"action":"send","session":"userbot:0.0","keys":["hi-host","Enter"]}`)
	if screen := call(t, caller, `{"action":"read","session":"userbot:0.0","wait_ms":300}`); !strings.Contains(screen, "hi-host") {
		t.Fatalf("host pane missing sent text: %q", screen)
	}
}
