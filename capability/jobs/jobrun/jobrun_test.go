package jobrun_test

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/invokr-agent/invokr/capability/jobs"
	"github.com/invokr-agent/invokr/capability/jobs/jobrun"
	sqlitepkg "github.com/invokr-agent/invokr/capability/memory/sqlite"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/testutil/providertest"
	"go.uber.org/zap"
)

func newTestDB(t *testing.T) *jobs.Store {
	t.Helper()
	db, err := sqlitepkg.InitDbAtPath(":memory:")
	if err != nil {
		t.Fatalf("InitDbAtPath: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return jobs.NewStore(db)
}

func newTestApp(t *testing.T, baseURL string) config.AppConfig {
	t.Helper()
	return config.AppConfig{
		Provider: config.ProviderConfig{BaseURL: baseURL, Model: "test-model"},
		Memory:   config.MemoryConfig{Dir: t.TempDir()},
	}
}

func addTestJob(t *testing.T, store *jobs.Store) jobs.Job {
	t.Helper()
	job, err := store.Add(context.Background(), "test-job", "* * * * *", "say hello", "deny")
	if err != nil {
		t.Fatalf("store.Add: %v", err)
	}
	return job
}

func TestRunJob_OK(t *testing.T) {
	srv := providertest.NewScripted(t, providertest.ReplyStep("hello"))
	defer srv.Close()

	store := newTestDB(t)
	job := addTestJob(t, store)
	app := newTestApp(t, srv.URL())

	err := jobrun.RunJob(context.Background(), app, store, job, noopLogger(t), false)
	if err != nil {
		t.Fatalf("RunJob returned error: %v", err)
	}

	got, err := store.GetByName(context.Background(), "test-job")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got.LastStatus == nil || *got.LastStatus != "ok" {
		t.Fatalf("expected LastStatus=ok, got %v", got.LastStatus)
	}
	if got.LastRunAt == nil {
		t.Fatal("expected LastRunAt to be set")
	}
}

func TestRunJob_ErrorStep(t *testing.T) {
	srv := providertest.NewScripted(t, providertest.ErrorStep(500, `{"error":"`+strings.Repeat("界", 300)+`"}`))
	defer srv.Close()

	store := newTestDB(t)
	job := addTestJob(t, store)
	app := newTestApp(t, srv.URL())

	err := jobrun.RunJob(context.Background(), app, store, job, noopLogger(t), false)
	if err == nil {
		t.Fatal("expected RunJob to return error on 500 step")
	}

	got, err := store.GetByName(context.Background(), "test-job")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got.LastStatus == nil || !strings.HasPrefix(*got.LastStatus, "error: ") {
		t.Fatalf("expected LastStatus to have prefix 'error: ', got %v", got.LastStatus)
	}
	if !utf8.ValidString(*got.LastStatus) || utf8.RuneCountInString(*got.LastStatus) > 255 {
		t.Fatalf("expected valid UTF-8 status of at most 255 runes, got %q", *got.LastStatus)
	}
}

func TestRunJob_CancelledContextRecordsError(t *testing.T) {
	store := newTestDB(t)
	job := addTestJob(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := jobrun.RunJob(ctx, newTestApp(t, "http://127.0.0.1"), store, job, noopLogger(t), false)
	if err == nil {
		t.Fatal("expected RunJob to return context cancellation error")
	}

	got, err := store.GetByName(context.Background(), "test-job")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got.LastStatus == nil || !strings.HasPrefix(*got.LastStatus, "error: ") {
		t.Fatalf("expected LastStatus to have prefix 'error: ', got %v", got.LastStatus)
	}
}

func TestRunJob_BootstrapFailureRecordsError(t *testing.T) {
	store := newTestDB(t)
	job := addTestJob(t, store)
	app := newTestApp(t, "http://127.0.0.1")
	app.Log.Format = "invalid"

	err := jobrun.RunJob(context.Background(), app, store, job, noopLogger(t), false)
	if err == nil {
		t.Fatal("expected RunJob to return bootstrap error")
	}

	got, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastStatus == nil || !strings.HasPrefix(*got.LastStatus, "error: bootstrap: ") {
		t.Fatalf("expected bootstrap error status, got %v", got.LastStatus)
	}
}

func TestRunJob_FailOnToolError(t *testing.T) {
	// Script a tool call to an unknown tool, then a reply.
	// The runner will attempt the tool call, fail, and report it in ToolFailures.
	srv := providertest.NewScripted(t,
		providertest.ToolCallStep("nonexistent_tool_xyzzy", `{}`),
		providertest.ReplyStep("done"),
	)
	defer srv.Close()

	store := newTestDB(t)
	job := addTestJob(t, store)
	app := newTestApp(t, srv.URL())

	err := jobrun.RunJob(context.Background(), app, store, job, noopLogger(t), true)
	if err == nil {
		t.Fatal("expected RunJob to return error when failOnToolError=true and tool failures present")
	}

	got, err := store.GetByName(context.Background(), "test-job")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got.LastStatus == nil {
		t.Fatal("expected LastStatus to be set")
	}
	if !strings.Contains(*got.LastStatus, "tool failure") {
		t.Fatalf("expected LastStatus to contain 'tool failure', got %q", *got.LastStatus)
	}
}

func noopLogger(t *testing.T) *zap.Logger {
	t.Helper()
	return zap.NewNop()
}
