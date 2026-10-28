package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/capability/jobs"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"

	"go.uber.org/zap"
)

type noticeRecorder struct {
	mu      sync.Mutex
	notices []string
}

func (n *noticeRecorder) Dispatch(_ context.Context, event events.Event) {
	if event.Notice == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notices = append(n.notices, event.Notice.Text)
}

func (n *noticeRecorder) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.notices...)
}

// newJobCommandService builds a CommandService wired only with what runJob
// touches: the jobs DB location, the session factory, and the notice sink.
func newJobCommandService(memDir string, newSession ChatSessionFunc) (*CommandService, *noticeRecorder) {
	rec := &noticeRecorder{}
	return &CommandService{
		app:        config.AppConfig{Memory: config.MemoryConfig{Dir: memDir}},
		newSession: newSession,
		events:     rec,
		running:    map[string]struct{}{},
		logger:     zap.NewNop(),
	}, rec
}

func addStoredJob(t *testing.T, memDir string) jobs.Job {
	t.Helper()
	db, store, err := jobs.OpenStore(config.AppConfig{Memory: config.MemoryConfig{Dir: memDir}})
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() { _ = db.Close() }()
	job, err := store.Add(context.Background(), "slash-job", "* * * * *", "do it", "deny")
	if err != nil {
		t.Fatalf("store.Add: %v", err)
	}
	return job
}

func storedStatus(t *testing.T, memDir, id string) *string {
	t.Helper()
	db, store, err := jobs.OpenStore(config.AppConfig{Memory: config.MemoryConfig{Dir: memDir}})
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() { _ = db.Close() }()
	job, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	return job.LastStatus
}

// The store is opened before the session is created, so a job whose DB cannot
// be opened never runs — and the failure is still reported as a notice.
func TestRunJobStoreOpenFailureSkipsRun(t *testing.T) {
	// A memory dir that is really a regular file cannot host the sqlite DB.
	memDir := filepath.Join(t.TempDir(), "memory-dir")
	if err := os.WriteFile(memDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	var called bool
	c, rec := newJobCommandService(memDir, func(string) (turn.IdentifiedSession, *approval.Broker, error) {
		called = true
		return nil, nil, nil
	})
	c.running["blocked"] = struct{}{}

	c.runJob(jobs.Job{ID: "missing", Name: "blocked", ApprovalPolicy: "deny"})

	if called {
		t.Fatal("session factory was called even though the store could not be opened")
	}
	if _, active := c.running["blocked"]; active {
		t.Fatal("runJob did not release its running-job slot")
	}
	notices := rec.all()
	if len(notices) != 1 || !strings.Contains(notices[0], "job blocked completed: error: ") {
		t.Fatalf("notices = %v, want one store-open error notice", notices)
	}
}

// A session-creation failure records the error status on the job and reports it.
func TestRunJobSessionFailureRecordsStatus(t *testing.T) {
	memDir := t.TempDir()
	job := addStoredJob(t, memDir)
	c, rec := newJobCommandService(memDir, func(string) (turn.IdentifiedSession, *approval.Broker, error) {
		return nil, nil, errors.New("no session for you")
	})
	c.running[job.Name] = struct{}{}

	c.runJob(job)

	status := storedStatus(t, memDir, job.ID)
	if status == nil || *status != "error: no session for you" {
		t.Fatalf("LastStatus = %v, want %q", status, "error: no session for you")
	}
	notices := rec.all()
	if len(notices) != 1 || !strings.Contains(notices[0], "no session for you") {
		t.Fatalf("notices = %v, want one session-failure notice", notices)
	}
	if _, active := c.running[job.Name]; active {
		t.Fatal("runJob did not release its running-job slot")
	}
}
