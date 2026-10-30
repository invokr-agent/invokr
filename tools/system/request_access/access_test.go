package request_access_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/testutil"
	"github.com/invokr-agent/invokr/tools"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/system/request_access"
)

// sessionCaller builds a session whose approver answers approved or denied.
func sessionCaller(t *testing.T, root string, approved bool) *tools.Caller {
	decision := approval.Decision{Approved: true}
	if !approved {
		decision = approval.Decision{Reason: "not my cache"}
	}
return tools.NewCaller(core.CallerDeps{
Policy: approval.NewPolicy(approval.Manual, false),
Approver: testutil.StaticApprover{Decision: decision},
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
		},
	})
}

func callAccess(t *testing.T, caller *tools.Caller, args string) (string, error) {
	return caller.HandleCall(context.Background(), &core.ToolCall{
		Name: "request_access", Arguments: json.RawMessage(args),
	})
}

// rw and ro are what a shell call of this session would mount beyond its roots.
// The tests run without a session ID, so "" is the session they grant in.
func rw() []string {
	out, _ := request_access.RequestAccessRoots("")
	return out
}

func ro() []string {
	_, out := request_access.RequestAccessRoots("")
	return out
}

// grantedPath is the directory the tool reported, in host form.
func grantedPath(t *testing.T, out string) string {
	var res map[string]string
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	return filepath.FromSlash(res["path"])
}

func request(t *testing.T, caller *tools.Caller, path, mode, scope string) (string, error) {
	args := `{"path":"` + path + `","mode":"` + mode + `","reason":"the lint cache the build writes to","scope":"` + scope + `"}`
	return callAccess(t, caller, args)
}

func TestWriteGrantIsKeptInMemoryForTheSession(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	caller := sessionCaller(t, root, true)
	out, err := request(t, caller, cache, "write", "session")
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	path := grantedPath(t, out)
	if !slices.Contains(rw(), path) {
		t.Fatalf("writable grants %v, want %s", rw(), path)
	}
	if slices.Contains(ro(), path) {
		t.Fatalf("a write grant landed in the read-only grants %v", ro())
	}
}

func TestReadGrantMountsReadOnly(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	caller := sessionCaller(t, root, true)
	out, err := request(t, caller, cache, "read", "session")
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	path := grantedPath(t, out)
	if !slices.Contains(ro(), path) {
		t.Fatalf("read grants %v, want %s", ro(), path)
	}
	if slices.Contains(rw(), path) {
		t.Fatalf("a read grant landed in the writable grants %v", rw())
	}
}

func TestDeniedRequestGrantsNothing(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	caller := sessionCaller(t, root, false)
	_, err := request(t, caller, cache, "write", "session")
	if err == nil {
		t.Fatal("a denied request granted access")
	}
	var denied *approval.DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err %v, want a denial", err)
	}
	if !strings.Contains(err.Error(), "not my cache") {
		t.Fatalf("denial %q, want the approver's reason", err)
	}
	if slices.Contains(rw(), filepath.Clean(cache)) {
		t.Fatal("a denied request left a writable grant")
	}
}

func TestNoApproverGrantsNothing(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
		},
	})
	_, err := request(t, caller, cache, "write", "session")
	if err == nil || !strings.Contains(err.Error(), "needs an approver") {
		t.Fatalf("err %v, want no-approver refusal", err)
	}
	if slices.Contains(rw(), filepath.Clean(cache)) {
		t.Fatal("an unapproved request left a writable grant")
	}
}

func TestOnceGrantCoversOneShellCall(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	caller := sessionCaller(t, root, true)
	out, err := request(t, caller, cache, "write", "once")
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	path := grantedPath(t, out)
	if !slices.Contains(rw(), path) {
		t.Fatalf("writable grants %v, want %s", rw(), path)
	}
	request_access.ExpireOnceGrants("")
	if slices.Contains(rw(), path) {
		t.Fatalf("a once grant survived the shell call that used it: %v", rw())
	}
}

func TestSessionGrantSurvivesOnceExpiry(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	caller := sessionCaller(t, root, true)
	out, err := request(t, caller, cache, "write", "session")
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	path := grantedPath(t, out)
	request_access.ExpireOnceGrants("")
	if !slices.Contains(rw(), path) {
		t.Fatalf("a session grant expired with the once grants: %v", rw())
	}
}

func TestRefusedPathsNeverReachApproval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, dir := range [][]string{{".ssh"}, {".gnupg"}, {".aws"}, {".config", "gh"}, {".invokr"}, {".cache"}, {".pki", "keys"}} {
		if err := os.MkdirAll(filepath.Join(append([]string{home}, dir...)...), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	caller := sessionCaller(t, root, true)
	refused := []string{
		"/",
		home,
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".aws"),
		filepath.Join(home, ".config", "gh"),
		filepath.Join(home, ".invokr"),
		filepath.Join(home, ".pki", "keys"),
		filepath.Join(home, "link"),
		filepath.Join(home, ".cache", "..", ".ssh"),
		"/usr/share/keyrings",
	}
	for _, path := range refused {
		_, err := request(t, caller, path, "write", "session")
		if err == nil || !strings.Contains(err.Error(), "cannot be opened to the agent") {
			t.Fatalf("path %s: err %v, want a refusal that names why", path, err)
		}
	}
}

func TestPathMustBeAnExistingDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "cache.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	caller := sessionCaller(t, root, true)
	_, err := request(t, caller, filepath.Join(home, "missing"), "read", "session")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing path: err %v, want it named as missing", err)
	}
	_, err = request(t, caller, filepath.Join(home, "cache.txt"), "read", "session")
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file path: err %v, want it refused as a file", err)
	}
	_, err = request(t, caller, "relative/cache", "read", "session")
	if err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("relative path: err %v, want it refused", err)
	}
	_, err = request(t, caller, root, "write", "session")
	if err == nil || !strings.Contains(err.Error(), "already writable") {
		t.Fatalf("workspace root: err %v, want it refused as already granted", err)
	}
}
