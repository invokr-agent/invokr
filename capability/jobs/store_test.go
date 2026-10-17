package jobs_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/invokr-ai/invokr/capability/jobs"
	sqlitepkg "github.com/invokr-ai/invokr/capability/memory/sqlite"
)

func newTestStore(t *testing.T) *jobs.Store {
	t.Helper()
	db, err := sqlitepkg.InitDbAtPath(":memory:")
	if err != nil {
		t.Fatalf("InitDbAtPath: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return jobs.NewStore(db)
}

func TestAdd_GetByName_RoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	got, err := st.Add(ctx, "daily-report", "0 8 * * *", "run daily report", "deny")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	fetched, err := st.GetByName(ctx, "daily-report")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}

	if fetched.ID != got.ID {
		t.Errorf("ID mismatch: %q vs %q", fetched.ID, got.ID)
	}
	byID, err := st.Get(ctx, got.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if byID != fetched {
		t.Errorf("Get result differs from GetByName: %#v vs %#v", byID, fetched)
	}
	if fetched.Name != "daily-report" {
		t.Errorf("Name: %q", fetched.Name)
	}
	if fetched.Schedule != "0 8 * * *" {
		t.Errorf("Schedule: %q", fetched.Schedule)
	}
	if fetched.Prompt != "run daily report" {
		t.Errorf("Prompt: %q", fetched.Prompt)
	}
	if fetched.ApprovalPolicy != "deny" {
		t.Errorf("ApprovalPolicy: %q", fetched.ApprovalPolicy)
	}
	if fetched.LastRunAt != nil {
		t.Error("LastRunAt should be nil")
	}
	if fetched.LastStatus != nil {
		t.Error("LastStatus should be nil")
	}
}

func TestAdd_DuplicateName(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if _, err := st.Add(ctx, "dup", "*/5 * * * *", "p", "deny"); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	_, err := st.Add(ctx, "dup", "*/5 * * * *", "p2", "deny")
	if !errors.Is(err, jobs.ErrDuplicateName) {
		t.Fatalf("expected ErrDuplicateName, got: %v", err)
	}
}

func TestUpdateDenyJobRefusesPrivilegedPolicy(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	job, err := store.Add(ctx, "privileged", "0 2 * * *", "original", "allow")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateDenyJob(ctx, job.ID, "0 3 * * *", "rewritten"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected guarded update to refuse privileged job, got %v", err)
	}
	got, err := store.Get(ctx, job.ID)
	if err != nil || got.Prompt != "original" || got.Schedule != "0 2 * * *" {
		t.Fatalf("privileged job changed: %+v, %v", got, err)
	}
}

func TestList_TwoJobs(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if _, err := st.Add(ctx, "job-a", "*/5 * * * *", "pa", "deny"); err != nil {
		t.Fatalf("Add job-a: %v", err)
	}
	if _, err := st.Add(ctx, "job-b", "0 1 * * *", "pb", "allow"); err != nil {
		t.Fatalf("Add job-b: %v", err)
	}

	list, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(list))
	}

	names := map[string]bool{list[0].Name: true, list[1].Name: true}
	if !names["job-a"] || !names["job-b"] {
		t.Errorf("unexpected names: %v", names)
	}
}

func TestRecordRun(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	j, err := st.Add(ctx, "run-test", "*/5 * * * *", "p", "deny")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := st.RecordRun(ctx, j.ID, "ok"); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	fetched, err := st.GetByName(ctx, "run-test")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if fetched.LastRunAt == nil {
		t.Error("LastRunAt should not be nil after RecordRun")
	}
	if fetched.LastStatus == nil || *fetched.LastStatus != "ok" {
		t.Errorf("LastStatus: %v", fetched.LastStatus)
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	j, err := st.Add(ctx, "to-delete", "*/5 * * * *", "p", "deny")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := st.Delete(ctx, j.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = st.GetByName(ctx, "to-delete")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got: %v", err)
	}
}

func TestDeleteAll(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if _, err := st.Add(ctx, "da-a", "*/5 * * * *", "p", "deny"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := st.Add(ctx, "da-b", "0 1 * * *", "p", "deny"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := st.DeleteAll(ctx); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}

	list, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}
}

func TestRecordRun_NonexistentID(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if err := st.RecordRun(ctx, "nonexistent-id", "ok"); err != nil {
		t.Fatalf("RecordRun on nonexistent id should return nil, got: %v", err)
	}
}

func TestSanitizeSchedule(t *testing.T) {
	cases := []struct {
		expr    string
		wantErr bool
	}{
		{"*/5 * * * *", false},
		{"0 2 * * 1-5", false},
		{"", true},
		{"not-a-cron", true},
		{"* * * * * *", true},
		{"0,30 9-17 1,15 1-6 1-5", false},
		{"60 * * * *", true},    // minute out of range
		{"* 24 * * *", true},    // hour out of range
		{"* * 0 * *", true},     // day-of-month out of range
		{"* * * 13 *", true},    // month out of range
		{"* * * * 8", true},     // day-of-week out of range
		{"* * * * a", true},     // invalid character
		{"*/x * * * *", true},   // invalid step
		{"1-2-3 * * * *", true}, // malformed range treated as non-numeric
	}

	for _, tc := range cases {
		err := jobs.SanitizeSchedule(tc.expr)
		if tc.wantErr && err == nil {
			t.Errorf("SanitizeSchedule(%q): expected error, got nil", tc.expr)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("SanitizeSchedule(%q): unexpected error: %v", tc.expr, err)
		}
	}
}
