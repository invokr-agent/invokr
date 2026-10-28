package schedule_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/capability/jobs"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/testutil"
	"github.com/invokr-agent/invokr/tools"
	"github.com/invokr-agent/invokr/tools/core"
)

func newCaller(t *testing.T, approved bool) *tools.Caller {
	t.Helper()
	return tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
			Memory:  config.MemoryConfig{Dir: t.TempDir()},
		},
		Approver: testutil.StaticApprover{Decision: approval.Decision{Approved: approved}},
	})
}

func call(t *testing.T, c *tools.Caller, args map[string]any) (string, error) {
	t.Helper()
	payload, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return c.HandleCall(context.Background(), &core.ToolCall{Name: "schedule", Arguments: payload})
}

func TestCallerRegistersScheduleTool(t *testing.T) {
	c := newCaller(t, true)
	found := false
	for _, spec := range c.Specs() {
		if spec.Name == "schedule" {
			found = true
			assertVerificationGuidance(t, spec.Description)
		}
	}
	if !found {
		t.Fatal("expected schedule tool to be registered")
	}
}

func TestScheduleDescriptionWarnsWithoutCrontab(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, spec := range newCaller(t, true).Specs() {
		if spec.Name == "schedule" && !strings.Contains(spec.Description, "no crontab binary") {
			t.Fatalf("expected missing-crontab warning, got %q", spec.Description)
		}
	}
}

func TestScheduleDeniedWithoutApproval(t *testing.T) {
	c := newCaller(t, false)
	_, err := call(t, c, map[string]any{"action": "list"})
	if err == nil {
		t.Fatal("expected denial error")
	}
}

func TestScheduleList_Empty(t *testing.T) {
	c := newCaller(t, true)
	out, err := call(t, c, map[string]any{"action": "list"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if out != "no scheduled jobs." {
		t.Fatalf("expected empty-list message, got %q", out)
	}
}

func TestScheduleUnknownAction(t *testing.T) {
	c := newCaller(t, true)
	_, err := call(t, c, map[string]any{"action": "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

// Exercise the crontab boundary without changing the user's real scheduled jobs.
func fakeCrontab(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "entries")
	testutil.MustWriteFile(t, path, "")
	binary := filepath.Join(dir, "crontab")
	testutil.MustWriteFile(t, binary, "#!/bin/sh\ncase \"$1\" in\n-l) /bin/cat \"$TEST_CRONTAB\" ;;\n-) /bin/cat > \"$TEST_CRONTAB\" ;;\n*) exit 2 ;;\nesac\n")
	if err := os.Chmod(binary, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("TEST_CRONTAB", path)
	return path
}

func assertVerificationGuidance(t *testing.T, out string) {
	t.Helper()
	for _, want := range []string{"command/probe once", "saved job once", "do not sleep, poll, or create marker jobs", "scheduled execution as unverified"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in %q", want, out)
		}
	}
}

func TestScheduleAddListModifyRemove(t *testing.T) {
	crontab := fakeCrontab(t)
	c := newCaller(t, true)

	out, err := call(t, c, map[string]any{
		"action": "add", "name": "nightly", "schedule": "0 2 * * *", "prompt": "say hi",
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(out, "nightly") || !strings.Contains(out, "unattended") {
		t.Fatalf("expected add result to name job and surface unattended note, got %q", out)
	}
	assertVerificationGuidance(t, out)

	out, err = call(t, c, map[string]any{"action": "list"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, `nightly | schedule="0 2 * * *" | last_status=unverified (no recorded run)`) {
		t.Fatalf("expected saved schedule with unverified execution, got %q", out)
	}
	assertVerificationGuidance(t, out)

	out, err = call(t, c, map[string]any{
		"action": "modify", "name": "nightly", "schedule": "0 3 * * *",
	})
	if err != nil {
		t.Fatalf("modify: %v", err)
	}
	if !strings.Contains(out, "modified") {
		t.Fatalf("expected modify confirmation, got %q", out)
	}
	assertVerificationGuidance(t, out)
	entries, err := os.ReadFile(crontab)
	if err != nil || strings.Count(string(entries), "# invokr-job:") != 1 || !strings.Contains(string(entries), "0 3 * * *") {
		t.Fatalf("expected one updated cron entry: %s, %v", entries, err)
	}
	out, err = call(t, c, map[string]any{"action": "modify", "name": "nightly"})
	if err != nil || !strings.Contains(out, "unmodified") {
		t.Fatalf("no-op modify: %q, %v", out, err)
	}
	assertVerificationGuidance(t, out)

	out, err = call(t, c, map[string]any{"action": "remove", "name": "nightly"})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(out, "removed") {
		t.Fatalf("expected remove confirmation, got %q", out)
	}
}

func TestScheduleRefusesDelegatedTiers(t *testing.T) {
	fakeCrontab(t)
	c := newCaller(t, true)
	_, err := call(t, c, map[string]any{
		"action": "add", "name": "health", "schedule": "*/5 * * * *",
		"prompt": "append health.log", "allow": []string{"execute_write_command"},
	})
	if err == nil {
		t.Fatal("schedule tool must not persist delegated tiers")
	}
	out, err := call(t, c, map[string]any{"action": "list"})
	if err != nil || out != "no scheduled jobs." {
		t.Fatalf("rejected job must not be saved: %q, %v", out, err)
	}
	_, err = call(t, c, map[string]any{
		"action": "add", "name": "safe", "schedule": "*/5 * * * *", "prompt": "say hi",
	})
	if err != nil {
		t.Fatalf("deny-only add: %v", err)
	}
	_, err = call(t, c, map[string]any{"action": "modify", "name": "safe", "allow": []string{"execute_write_command"}})
	if err == nil {
		t.Fatal("schedule tool must not widen existing job policy")
	}
}

func TestScheduleRefusesPrivilegedJobModify(t *testing.T) {
	fakeCrontab(t)
	app := config.AppConfig{Toolbox: config.ToolboxConfig{Dir: t.TempDir()}, Memory: config.MemoryConfig{Dir: t.TempDir()}}
	c := tools.NewCaller(core.CallerDeps{App: app, Approver: testutil.StaticApprover{Decision: approval.Decision{Approved: true}}})
	db, store, err := jobs.OpenStore(app)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	stored, err := store.Add(context.Background(), "privileged", "0 2 * * *", "original", "allow")
	if err != nil {
		t.Fatal(err)
	}
	_, err = call(t, c, map[string]any{"action": "modify", "name": "privileged", "prompt": "rewrite"})
	if err == nil {
		t.Fatal("schedule tool must not rewrite a privileged job")
	}
	job, err := store.GetByName(context.Background(), "privileged")
	if err != nil || job.Prompt != "original" || job.ApprovalPolicy != "allow" {
		t.Fatalf("privileged job changed: %+v, %v", job, err)
	}
	if err := store.RecordRun(context.Background(), stored.ID, "denied: shell: job policy deny"); err != nil {
		t.Fatal(err)
	}
	out, err := call(t, c, map[string]any{"action": "list"})
	if err != nil || !strings.Contains(out, "last_status=denied: shell: job policy deny") {
		t.Fatalf("list omitted last-run denial: %q, %v", out, err)
	}
}

func TestScheduleUnavailableCrontabDoesNotClaimRegistration(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	c := newCaller(t, true)
	out, err := call(t, c, map[string]any{
		"action": "add", "name": "health", "schedule": "*/5 * * * *", "prompt": "run health probe",
	})
	if err == nil || !strings.Contains(err.Error(), "crontab binary not found") || strings.Contains(out, "added") {
		t.Fatalf("expected unavailable crontab error: %q, %v", out, err)
	}
	out, err = call(t, c, map[string]any{"action": "list"})
	if err != nil || out != "no scheduled jobs." {
		t.Fatalf("failed install must roll back registration: %q, %v", out, err)
	}
}
