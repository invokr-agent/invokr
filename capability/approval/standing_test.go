package approval

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func testGrant(t *testing.T, pattern string) Grant {
	t.Helper()
	return Grant{
		Tool:       "shell",
		ArgPattern: pattern,
		Scope:      ScopeGlobal,
		ExpiresAt:  time.Now().Add(time.Hour),
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "grants.json"))
}

func mustAdd(t *testing.T, s *Store, g Grant) Grant {
	t.Helper()
	stored, err := s.Add(g)
	if err != nil {
		t.Fatalf("Add(%+v): %v", g, err)
	}
	return stored
}

// TestGlobMatchAcceptsIntendedCalls covers the patterns a grant is meant to
// cover. Every other test in this file is a negative case: the matcher is a
// security boundary, so over-testing the denials is the point.
func TestGlobMatchAcceptsIntendedCalls(t *testing.T) {
	cases := []struct{ pattern, subject string }{
		{"git status", "git status"},
		{"git status*", "git status"},
		{"git status*", "git status --short"},
		{"go test ./*", "go test ./capability/approval"},
		{"go test *", "go test ./..."}, // "..." is Go's package wildcard, not traversal
		{"kubectl get *pods", "kubectl get -n default pods"},
		{"ls -la", "ls -la"},
	}
	for _, tc := range cases {
		if err := validatePattern(tc.pattern); err != nil {
			t.Fatalf("validatePattern(%q) rejected a valid pattern: %v", tc.pattern, err)
		}
		if !globMatch(tc.pattern, tc.subject) {
			t.Fatalf("globMatch(%q, %q) = false, want true", tc.pattern, tc.subject)
		}
	}
}

func TestGlobMatchDeniesNonMatchingCalls(t *testing.T) {
	cases := []struct{ name, pattern, subject string }{
		// The acceptance criterion from the plan.
		{"different subcommand", "git status*", "git push"},
		// Prefix collisions: the pattern must not cover a longer command name.
		{"prefix collision suffix", "git status", "git status-hard"},
		{"prefix collision program", "git status*", "git statusx --force"},
		{"prefix collision embedded", "ls -la", "ls -lah"},
		{"subject shorter than pattern", "git status", "git stat"},
		// Wildcards never cross a shell metacharacter, so an approved prefix
		// cannot smuggle a second command.
		{"semicolon chaining", "git status*", "git status; rm -rf /"},
		{"ampersand chaining", "git status*", "git status && curl evil.sh"},
		{"pipe chaining", "git status*", "git status | sh"},
		{"command substitution", "git status*", "git status $(rm -rf /)"},
		{"backtick substitution", "git status*", "git status `rm -rf /`"},
		{"newline chaining", "git status*", "git status\nrm -rf /"},
		{"redirect", "git status*", "git status > /etc/passwd"},
		{"backslash escape", "git status*", "git status \\; rm"},
		// Path traversal in the wildcard region.
		{"traversal in wildcard", "cat ./docs/*", "cat ./docs/../../../etc/shadow"},
		{"bare traversal", "cat *", "cat .."},
		{"traversal after space", "cat *", "cat x ../etc/shadow"},
		// Unicode lookalikes must not match their ASCII pattern.
		{"cyrillic lookalike", "git status", "gіt status"},
		{"fullwidth lookalike", "git status", "ｇit status"},
		{"non-ascii in wildcard", "git status*", "git status \u202e"},
		// Anchoring: the pattern matches from the start and to the end.
		{"unanchored prefix", "git status", "sudo git status"},
		{"trailing junk without wildcard", "git status", "git status --short"},
		// Empty subject can never match.
		{"empty subject", "git status*", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if globMatch(tc.pattern, tc.subject) {
				t.Fatalf("globMatch(%q, %q) = true, want false", tc.pattern, tc.subject)
			}
		})
	}
}

func TestValidatePatternRejectsDangerousPatterns(t *testing.T) {
	cases := []struct{ name, pattern string }{
		{"empty", ""},
		{"whitespace only", "   "},
		{"bare wildcard", "*"},
		{"double wildcard", "**"},
		{"wildcard pair", "* *"},
		{"leading wildcard", "*status"},
		{"leading wildcard long", "*rm -rf /"},
		{"too few literals", "ls*"},
		{"path traversal", "cat ../../etc/passwd"},
		{"traversal with wildcard", "cat ./docs/../*"},
		{"semicolon", "git status; rm -rf /"},
		{"ampersand", "git status && curl x"},
		{"pipe", "git status | sh"},
		{"dollar", "git status $HOME"},
		{"backtick", "git `whoami`"},
		{"backslash", "git status \\"},
		{"newline", "git status\nrm -rf /"},
		{"unicode lookalike", "gіt status"},
		{"emoji", "git status 🎉"},
		{"control char", "git status\x00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePattern(tc.pattern)
			if err == nil {
				t.Fatalf("validatePattern(%q) = nil, want rejection", tc.pattern)
			}
			var invalid *ErrInvalidPattern
			if !errors.As(err, &invalid) {
				t.Fatalf("validatePattern(%q) error type = %T, want *ErrInvalidPattern", tc.pattern, err)
			}
		})
	}
}

func TestStoreMatchApprovesGrantedCallOnly(t *testing.T) {
	store := newTestStore(t)
	mustAdd(t, store, testGrant(t, "git status*"))
	if _, ok := store.Match(Request{Tool: "shell", Subject: "git status --short"}); !ok {
		t.Fatalf("granted call was not matched")
	}
	if _, ok := store.Match(Request{Tool: "shell", Subject: "git push"}); ok {
		t.Fatalf("git push was matched by a `git status*` grant")
	}
	if _, ok := store.Match(Request{Tool: "code_interpreter", Subject: "git status"}); ok {
		t.Fatalf("grant leaked to a different tool")
	}
	if _, ok := store.Match(Request{Tool: "shell"}); ok {
		t.Fatalf("request without a subject was matched")
	}
}

func TestStoreMatchNilStoreDeniesByDefault(t *testing.T) {
	var store *Store
	if _, ok := store.Match(Request{Tool: "shell", Subject: "git status"}); ok {
		t.Fatalf("nil store matched")
	}
}

func TestStoreMatchRespectsTier(t *testing.T) {
	store := newTestStore(t)
	grant := testGrant(t, "git status*")
	grant.Tier = "readonly"
	mustAdd(t, store, grant)
	if _, ok := store.Match(Request{Tool: "shell", Tier: "readonly", Subject: "git status"}); !ok {
		t.Fatalf("matching tier was not matched")
	}
	if _, ok := store.Match(Request{Tool: "shell", Tier: "network", Subject: "git status"}); ok {
		t.Fatalf("grant applied across tiers")
	}
}

func TestStoreMatchExpiredGrantDoesNotApply(t *testing.T) {
	store := newTestStore(t)
	grant := testGrant(t, "git status*")
	grant.ExpiresAt = time.Now().Add(-time.Second)
	if _, err := store.Add(grant); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, ok := store.Match(Request{Tool: "shell", Subject: "git status"}); ok {
		t.Fatalf("expired grant matched")
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("List returned %d expired grants, want 0", len(got))
	}
}

func TestStoreAddRequiresExpiry(t *testing.T) {
	store := newTestStore(t)
	grant := testGrant(t, "git status*")
	grant.ExpiresAt = time.Time{}
	if _, err := store.Add(grant); err == nil {
		t.Fatalf("Add accepted a grant with no expiry")
	}
}

func TestStoreAddRefusesExcludedTools(t *testing.T) {
	store := newTestStore(t)
	for _, tool := range []string{"create_tool", "delegate_task", "code_interpreter"} {
		grant := testGrant(t, "anything goes*")
		grant.Tool = tool
		_, err := store.Add(grant)
		var excluded *ErrExcluded
		if !errors.As(err, &excluded) {
			t.Fatalf("Add(%s) error = %v, want *ErrExcluded", tool, err)
		}
	}
}

// shell is the tool standing grants exist for; it must never be on the static
// exclusion list, and a path pattern must be able to cover an apply_patch call.
func TestStoreCoversShellAndFilePaths(t *testing.T) {
	store := newTestStore(t)
	mustAdd(t, store, testGrant(t, "git status*"))
	if _, ok := store.Match(Request{Tool: "shell", Subject: "git status --short"}); !ok {
		t.Fatal("shell grant did not match")
	}
	patch := testGrant(t, "docs/*")
	patch.Tool = "apply_patch"
	mustAdd(t, store, patch)
	if _, ok := store.Match(Request{Tool: "apply_patch", Subject: "docs/one.md"}); !ok {
		t.Fatal("apply_patch path grant did not match")
	}
	if _, ok := store.Match(Request{Tool: "apply_patch", Subject: "secrets/one.md"}); ok {
		t.Fatal("apply_patch grant matched a path outside the pattern")
	}
}

// A path-list subject must match per path: a wildcard expansion may not
// swallow the space separator and hide a second, out-of-pattern path.
func TestStoreMatchesPathListSubjectsPerPath(t *testing.T) {
	store := newTestStore(t)
	patch := testGrant(t, "docs/*")
	patch.Tool = "apply_patch"
	mustAdd(t, store, patch)
	twoPath := Request{
		Tool:              "apply_patch",
		Subject:           "docs/a.md .github/workflows/ci.yml",
		SubjectIsPathList: true,
	}
	if _, ok := store.Match(twoPath); ok {
		t.Fatal("grant matched a multi-path subject with an out-of-pattern path")
	}
	allIn := Request{
		Tool:              "apply_patch",
		Subject:           "docs/a.md docs/b.md",
		SubjectIsPathList: true,
	}
	if _, ok := store.Match(allIn); !ok {
		t.Fatal("grant did not match a multi-path subject with all paths in pattern")
	}
}

func TestStoreMatchIgnoresGrantsForLaterExcludedTools(t *testing.T) {
	store := newTestStore(t)
	mustAdd(t, store, testGrant(t, "git status*"))
	store.Exclude("shell")
	if _, ok := store.Match(Request{Tool: "shell", Subject: "git status"}); ok {
		t.Fatalf("grant applied to a tool added to the exclusion list")
	}
}

func TestStoreWorkspaceScope(t *testing.T) {
	store := newTestStore(t)
	grant := testGrant(t, "git status*")
	grant.Scope = ScopeWorkspace
	grant.Workspace = "/repo/a"
	mustAdd(t, store, grant)

	req := Request{Tool: "shell", Subject: "git status"}
	if _, ok := store.Match(req); ok {
		t.Fatalf("workspace grant matched with no workspace set")
	}
	store.SetWorkspace("/repo/b")
	if _, ok := store.Match(req); ok {
		t.Fatalf("workspace grant matched in a different workspace")
	}
	store.SetWorkspace("/repo/a")
	if _, ok := store.Match(req); !ok {
		t.Fatalf("workspace grant did not match in its own workspace")
	}
}

func TestStoreAddRejectsUnknownScope(t *testing.T) {
	store := newTestStore(t)
	grant := testGrant(t, "git status*")
	grant.Scope = "everywhere"
	if _, err := store.Add(grant); err == nil {
		t.Fatalf("Add accepted an unknown scope")
	}
	grant.Scope = ScopeWorkspace
	grant.Workspace = ""
	if _, err := store.Add(grant); err == nil {
		t.Fatalf("Add accepted a workspace grant with no workspace")
	}
}

func TestStorePersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	store := NewStore(path)
	stored := mustAdd(t, store, testGrant(t, "git status*"))

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat grants file: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("grants file mode = %v, want 0600", info.Mode().Perm())
	}

	reloaded := NewStore(path)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	grants := reloaded.List()
	if len(grants) != 1 || grants[0].ID != stored.ID {
		t.Fatalf("reloaded grants = %+v, want the one grant %s", grants, stored.ID)
	}
	if _, ok := reloaded.Match(Request{Tool: "shell", Subject: "git status"}); !ok {
		t.Fatalf("reloaded grant did not match")
	}
}

func TestStoreLoadPrunesExpiredAndInvalidGrants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	// Hand-written file: an expired grant, a bare-wildcard grant, and a grant
	// for an excluded tool must all be dropped on load.
	body := `{"version":2,"rules":[
	  {"id":"expired","tool":"shell","arg_pattern":"git status*","scope":"global","expires_at":"2000-01-01T00:00:00Z"},
	  {"id":"wildcard","tool":"shell","arg_pattern":"*","scope":"global","expires_at":"2999-01-01T00:00:00Z"},
	  {"id":"excluded","tool":"create_tool","arg_pattern":"make me a tool*","scope":"global","expires_at":"2999-01-01T00:00:00Z"},
	  {"id":"good","tool":"shell","arg_pattern":"git status*","scope":"global","expires_at":"2999-01-01T00:00:00Z"}
	]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write grants file: %v", err)
	}
	store := NewStore(path)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	grants := store.List()
	if len(grants) != 1 || grants[0].ID != "good" {
		t.Fatalf("loaded grants = %+v, want only \"good\"", grants)
	}
}

func TestStoreLoadCorruptFileYieldsNoGrants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write grants file: %v", err)
	}
	store := NewStore(path)
	if err := store.Load(); err == nil {
		t.Fatalf("Load of a corrupt file returned nil error")
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("corrupt file produced %d grants, want 0", len(got))
	}
	if _, ok := store.Match(Request{Tool: "shell", Subject: "git status"}); ok {
		t.Fatalf("corrupt file produced a matching grant")
	}
}

func TestStoreLoadMissingFileIsNotAnError(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "nope", "grants.json"))
	if err := store.Load(); err != nil {
		t.Fatalf("Load of a missing file: %v", err)
	}
}

func TestStoreSessionScopeIsNotPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	store := NewStore(path)
	grant := testGrant(t, "git status*")
	grant.Scope = ScopeSession
	mustAdd(t, store, grant)
	if _, ok := store.Match(Request{Tool: "shell", Subject: "git status"}); !ok {
		t.Fatalf("session grant did not match in its own session")
	}
	reloaded := NewStore(path)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := reloaded.List(); len(got) != 0 {
		t.Fatalf("session grant survived a reload: %+v", got)
	}
}

func TestStoreRevokeTakesEffectImmediately(t *testing.T) {
	store := newTestStore(t)
	stored := mustAdd(t, store, testGrant(t, "git status*"))
	req := Request{Tool: "shell", Subject: "git status"}
	if _, ok := store.Match(req); !ok {
		t.Fatalf("grant did not match before revoke")
	}
	removed, err := store.Revoke(stored.ID)
	if !removed || err != nil {
		t.Fatalf("Revoke = (%v, %v), want (true, nil)", removed, err)
	}
	if _, ok := store.Match(req); ok {
		t.Fatalf("revoked grant still matched")
	}
	if removed, _ := store.Revoke(stored.ID); removed {
		t.Fatalf("second Revoke reported a removal")
	}
}

// unwritableStore returns a store whose grants file cannot be written.
func unwritableStore(t *testing.T) *Store {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission-based failure injection needs a non-root POSIX host")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return NewStore(filepath.Join(dir, "grants.json"))
}

// TestStoreAddRollsBackWhenPersistenceFails: a caller that sees an error from
// Add must not be left with a live grant that auto-approves anyway.
func TestStoreAddRollsBackWhenPersistenceFails(t *testing.T) {
	store := unwritableStore(t)
	if _, err := store.Add(testGrant(t, "git status*")); err == nil {
		t.Fatalf("Add to an unwritable path returned nil error")
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("failed Add left %d grants in memory, want 0", len(got))
	}
	if _, ok := store.Match(Request{Tool: "shell", Subject: "git status"}); ok {
		t.Fatalf("failed Add still auto-approved the call")
	}
}

// TestStoreRevokeReportsPersistenceFailure: a revoke that cannot be persisted
// takes effect in memory but must not report unqualified success — the grant is
// still in the file and would return on the next load.
func TestStoreRevokeReportsPersistenceFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grants.json")
	store := NewStore(path)
	stored := mustAdd(t, store, testGrant(t, "git status*"))

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission-based failure injection needs a non-root POSIX host")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	removed, err := store.Revoke(stored.ID)
	if !removed {
		t.Fatalf("Revoke reported no grant removed")
	}
	if err == nil {
		t.Fatalf("Revoke that could not persist returned a nil error")
	}
	if _, ok := store.Match(Request{Tool: "shell", Subject: "git status"}); ok {
		t.Fatalf("revoked grant still auto-approved in memory")
	}
}

// TestStoreLoadDropsGrantsExcludedBeforeLoad locks in the bootstrap ordering:
// Exclude runs before Load, so grants on disk for a capability-excluded tool
// never enter memory.
func TestStoreLoadDropsGrantsExcludedBeforeLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	body := `{"version":2,"rules":[{"id":"a","tool":"code_interpreter","arg_pattern":"print hello*","scope":"global","expires_at":"2999-01-01T00:00:00Z"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write grants file: %v", err)
	}
	store := NewStore(path)
	store.Exclude("code_interpreter")
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("loaded %d grants for an excluded tool, want 0", len(got))
	}
}

func TestStoreMatchEmitsAuditEvent(t *testing.T) {
	store := newTestStore(t)
	stored := mustAdd(t, store, testGrant(t, "git status*"))
	var mu sync.Mutex
	var seen []Grant
	store.SetAuditor(func(g Grant, _ Request, event string) {
		if event != "used" {
			return
		}
		mu.Lock()
		seen = append(seen, g)
		mu.Unlock()
	})
	if _, ok := store.Match(Request{Tool: "shell", Subject: "git status"}); !ok {
		t.Fatalf("grant did not match")
	}
	if _, ok := store.Match(Request{Tool: "shell", Subject: "git push"}); ok {
		t.Fatalf("git push matched")
	}
	if len(seen) != 1 || seen[0].ID != stored.ID {
		t.Fatalf("audit callbacks = %+v, want exactly one for %s", seen, stored.ID)
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	store := newTestStore(t)
	mustAdd(t, store, testGrant(t, "git status*"))
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store.Match(Request{Tool: "shell", Subject: "git status"})
			store.List()
			if i%4 == 0 {
				_, _ = store.Add(testGrant(t, "go build ./*"))
			}
		}(i)
	}
	wg.Wait()
}

func TestQueueAutoApprovesFromStandingGrant(t *testing.T) {
	store := newTestStore(t)
	mustAdd(t, store, testGrant(t, "git status*"))
	queue := &ApprovalQueue{}
	queue.SetStandingGrants(store)

	granted := Pending{Request: Request{ID: "1", Tool: "shell", Subject: "git status"}, decision: make(chan Decision, 1)}
	queue.enqueue(granted)
	select {
	case decision := <-granted.decision:
		if !decision.Approved {
			t.Fatalf("granted call was not auto-approved: %+v", decision)
		}
	default:
		t.Fatalf("granted call was queued instead of auto-approved")
	}

	other := Pending{Request: Request{ID: "2", Tool: "shell", Subject: "git push"}, decision: make(chan Decision, 1)}
	queue.enqueue(other)
	if queue.Len() != 1 {
		t.Fatalf("ungranted call queue length = %d, want 1", queue.Len())
	}
}

// The non-interactive denial must name the policy and the remedy: a bare
// "denied" reads to the model as a missing capability (#304).
func TestDenyReasonNamesPolicyAndRemedy(t *testing.T) {
	reason := DenyReason(Request{Tool: "shell", Tier: "execute_write_command"})
	want := "approval required: re-run with --allow write, or add a standing grant"
	if reason != want {
		t.Fatalf("DenyReason = %q, want %q", reason, want)
	}
	if got := DenyReason(Request{Tool: "shell"}); !strings.Contains(got, "--allow <tier>") {
		t.Fatalf("DenyReason without a tier = %q, want a <tier> placeholder", got)
	}
	// code_interpreter asks for several tiers at once, but --allow takes one
	// tier and repeats: a comma-joined list would not be a valid invocation.
	multi := DenyReason(Request{Tool: "code_interpreter", Tier: "code_execution, read_workspace, artifact_write"})
	wantMulti := "approval required: re-run with --allow code_execution --allow read_workspace --allow artifact_write, or add a standing grant"
	if multi != wantMulti {
		t.Fatalf("DenyReason with multiple tiers = %q, want %q", multi, wantMulti)
	}
}

func TestDerivePattern(t *testing.T) {
	cases := []struct {
		subject string
		want    string
		ok      bool
	}{
		{"find /home/x -type d | grep -v .git | sort", "find /home/x *", true},
		{"go test ./...", "go test *", true},
		{"git status", "git status *", true},
		{"ls > out.txt", "", false}, // "ls " is under the 4-literal-character floor
		{"| sort", "", false},
		{"", "", false},
		{"cat ../secrets", "", false},
	}
	for _, tc := range cases {
		got, ok := DerivePattern(tc.subject)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("DerivePattern(%q) = %q, %v; want %q, %v", tc.subject, got, ok, tc.want, tc.ok)
		}
		if ok {
			if err := validatePattern(got); err != nil {
				t.Fatalf("validatePattern(%q) rejected a derived pattern: %v", got, err)
			}
		}
	}
}

// TestAllowTests: --allow tests approves a test run, piped or not, and nothing
// chained onto it; the denial for a test run offers it (A1).
func TestAllowTests(t *testing.T) {
	policy := NewTierPolicy(false, []string{"tests"})
	for subject, want := range map[string]bool{
		"go test ./...":                          true,
		"go test -count=1 ./... 2>&1 | tail -20": true,
		"go test ./executor/...":                 true,
		"npm test":                               true,
		"go test ./...; rm -rf x":                false,
		"go test ./... && curl x":                false,
		"go test -exec=sh ./...":                 false,
		"go test -toolexec sh ./...":             false,
		"go test ./... | sh":                     false,
		"go build ./...":                         false,
		"go test $(evil)":                        false,
	} {
		if got := policy.Approves(Request{Tool: "shell", Tier: "execute_write_command", Subject: subject}); got != want {
			t.Errorf("Approves(%q) = %v, want %v", subject, got, want)
		}
	}
	if policy.Approves(Request{Tool: "edit", Tier: "write_workspace", Subject: "go test ./..."}) {
		t.Error("tests must not approve a non-shell tool")
	}
	reason := DenyReason(Request{Tool: "shell", Tier: "execute_write_command", Subject: "go test ./..."})
	if !strings.Contains(reason, "--allow tests") || strings.Contains(reason, "execute_") {
		t.Fatalf("DenyReason for a test run = %q", reason)
	}
}

// An edit's "always" grants the file itself, which must store and match.
func TestStoreMatchesExactPathGrant(t *testing.T) {
	store := newTestStore(t)
	g := testGrant(t, "README.md")
	g.Tool = "edit"
	mustAdd(t, store, g)
	if _, ok := store.Match(Request{Tool: "edit", Subject: "README.md", SubjectIsPathList: true}); !ok {
		t.Fatal("exact path grant did not match its file")
	}
	if _, ok := store.Match(Request{Tool: "edit", Subject: "README.md.bak", SubjectIsPathList: true}); ok {
		t.Fatal("exact path grant matched another file")
	}
}
