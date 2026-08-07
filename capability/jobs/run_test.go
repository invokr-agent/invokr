package jobs_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/capability/jobs"
	sqlitepkg "github.com/invokr-ai/invokr/capability/memory/sqlite"
	"github.com/invokr-ai/invokr/platform/turn"
)

type fakeSession struct {
	resp    turn.AgentResponse
	err     error
	approve bool // when set, request one approval from the broker before replying
	broker  *approval.Broker
	decided *approval.Decision
}

func (f *fakeSession) Send(ctx context.Context, prompt string) (turn.AgentResponse, error) {
	if f.approve {
		d, err := f.broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		var denied *approval.DeniedError
		if err != nil && !errors.As(err, &denied) {
			return turn.AgentResponse{}, err
		}
		f.decided = &d
	}
	return f.resp, f.err
}

func newRunStore(t *testing.T) *jobs.Store {
	t.Helper()
	db, err := sqlitepkg.InitDbAtPath(":memory:")
	if err != nil {
		t.Fatalf("InitDbAtPath: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return jobs.NewStore(db)
}

func addRunJob(t *testing.T, store *jobs.Store, policy string) jobs.Job {
	t.Helper()
	job, err := store.Add(context.Background(), "run-job", "* * * * *", "say hello", policy)
	if err != nil {
		t.Fatalf("store.Add: %v", err)
	}
	return job
}

func lastStatus(t *testing.T, store *jobs.Store) string {
	t.Helper()
	got, err := store.GetByName(context.Background(), "run-job")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got.LastStatus == nil {
		t.Fatal("expected LastStatus to be set")
	}
	return *got.LastStatus
}

func TestRun_OKRecordsStatus(t *testing.T) {
	store := newRunStore(t)
	job := addRunJob(t, store, "deny")
	session := &fakeSession{resp: turn.AgentResponse{Message: "hello"}}

	if err := jobs.Run(context.Background(), session, approval.NewBroker(), store, job, jobs.RunOptions{}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := lastStatus(t, store); got != "ok" {
		t.Fatalf("expected LastStatus=ok, got %q", got)
	}
}

func TestRun_SendErrorRecordsTruncatedStatus(t *testing.T) {
	store := newRunStore(t)
	job := addRunJob(t, store, "deny")
	session := &fakeSession{err: errors.New(strings.Repeat("界", 300))}

	if err := jobs.Run(context.Background(), session, approval.NewBroker(), store, job, jobs.RunOptions{}); err == nil {
		t.Fatal("expected Run to return the send error")
	}
	status := lastStatus(t, store)
	if !strings.HasPrefix(status, "error: ") {
		t.Fatalf("expected LastStatus prefix 'error: ', got %q", status)
	}
	if !utf8.ValidString(status) || utf8.RuneCountInString(status) > 255 {
		t.Fatalf("expected valid UTF-8 status of at most 255 runes, got %q", status)
	}
}

func TestRun_FailOnToolError(t *testing.T) {
	store := newRunStore(t)
	job := addRunJob(t, store, "deny")
	session := &fakeSession{resp: turn.AgentResponse{
		ToolFailures: []turn.ToolOutcome{{Tool: "shell", Kind: turn.OutcomeKindError, Detail: "boom"}},
	}}

	err := jobs.Run(context.Background(), session, approval.NewBroker(), store, job, jobs.RunOptions{FailOnToolError: true})
	if err == nil {
		t.Fatal("expected Run to fail when FailOnToolError is set and failures exist")
	}
	if got := lastStatus(t, store); !strings.Contains(got, "tool failure") {
		t.Fatalf("expected LastStatus to mention tool failures, got %q", got)
	}
}

func TestRun_DrainsApprovalsPerPolicy(t *testing.T) {
	for policy, want := range map[string]bool{"allow": true, "deny": false} {
		store := newRunStore(t)
		job, err := store.Add(context.Background(), "run-job", "* * * * *", "p", policy)
		if err != nil {
			t.Fatalf("store.Add: %v", err)
		}
		broker := approval.NewBroker()
		session := &fakeSession{approve: true, broker: broker}

		if err := jobs.Run(context.Background(), session, broker, store, job, jobs.RunOptions{}); err != nil {
			t.Fatalf("Run(%s) returned error: %v", policy, err)
		}
		if session.decided == nil {
			t.Fatalf("Run(%s): approval request was not decided", policy)
		}
		if session.decided.Approved != want {
			t.Fatalf("Run(%s): approved=%v, want %v", policy, session.decided.Approved, want)
		}
	}
}
