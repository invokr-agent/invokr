package approval

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type modeApprover func(context.Context, Request) (Decision, error)

func (f modeApprover) RequestApproval(ctx context.Context, req Request) (Decision, error) {
	return f(ctx, req)
}

func TestModeAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		mode                         Mode
		req                          Request
		ask, publish, prompt, denied bool
	}{
		{"manual-auto", Manual, Request{Writes: true}, false, false, false, false},
		{"manual-ask", Manual, Request{Writes: true}, true, false, true, false},
		{"auto-edit", Auto, Request{Writes: true, Confined: true}, true, false, false, false},
		{"auto-network", Auto, Request{Network: true, Confined: true}, true, false, false, false},
		{"auto-host", Auto, Request{Writes: true}, false, false, true, false},
		{"auto-protected", Auto, Request{Writes: true, Confined: true, Protected: true}, true, false, true, false},
		{"auto-publish", Auto, Request{Publishes: true, Network: true, Confined: true}, true, false, true, false},
		{"auto-publish-enabled", Auto, Request{Publishes: true, Network: true, Confined: true}, true, true, false, false},
		{"plan-read", Plan, Request{ReadOnly: true}, true, false, false, false},
		{"plan-check", Plan, Request{Confined: true}, true, false, false, false},
		{"plan-network", Plan, Request{Confined: true, Network: true}, false, false, true, false},
		{"plan-write", Plan, Request{Confined: true, Writes: true}, false, false, false, true},
		{"plan-host", Plan, Request{}, false, false, false, true},
		{"plan-publish", Plan, Request{Confined: true, Publishes: true}, true, true, false, true},
		{"yolo", YOLO, Request{Writes: true, Publishes: true, Protected: true}, true, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := NewPolicy(tc.mode, tc.publish)
			ctx, done := policy.Begin(context.Background(), tc.req)
			defer done()
			called := false
			approver := modeApprover(func(_ context.Context, req Request) (Decision, error) {
				called = true
				if !req.PolicyChecked {
					t.Error("queue could bypass the scoped prompt")
				}
				return Decision{Approved: true}, nil
			})
			handled, err := Authorize(ctx, approver, tc.req, tc.ask)
			if !handled || (err != nil) != tc.denied || called != tc.prompt {
				t.Fatalf("handled=%v err=%v prompted=%v", handled, err, called)
			}
		})
	}
}

func TestModesDenyAutomaticCallsAndFailClosedOnRuleLoad(t *testing.T) {
	for _, mode := range []Mode{Manual, Auto, Plan, YOLO} {
		t.Run(string(mode), func(t *testing.T) {
			store := newTestStore(t)
			mustAdd(t, store, denial(t, "git status*"))
			p := NewPolicy(mode, true)
			p.SetStore(store, nil)
			req := Request{Tool: "shell", Subject: "git status", ReadOnly: true, Confined: true}
			ctx, done := p.Begin(context.Background(), req)
			defer done()
			if _, err := Authorize(ctx, nil, req, false); err == nil {
				t.Fatal("denial bypassed")
			}
			p.SetStore(nil, errors.New("corrupt rules"))
			if _, err := Authorize(ctx, nil, req, false); (err != nil) != (mode != Manual) {
				t.Fatalf("unexpected unreadable-rules result: %v", err)
			}
		})
	}
}

func TestPendingApprovalSurvivesPublishingChange(t *testing.T) {
	p := NewPolicy(Auto, false)
	req := Request{Tool: "shell", Subject: "git push", Publishes: true, Confined: true}
	ctx, done := p.Begin(context.Background(), req)
	defer done()
	a := modeApprover(func(context.Context, Request) (Decision, error) {
		p.SetPublishing(true)
		return Decision{Approved: true}, nil
	})
	if _, err := Authorize(ctx, a, req, true); err != nil {
		t.Fatal(err)
	}
}

func TestPendingApprovalCannotCrossModeChange(t *testing.T) {
	p := NewPolicy(Manual, false)
	req := Request{Tool: "edit", Subject: "a.go", Writes: true, Confined: true}
	ctx, done := p.Begin(context.Background(), req)
	defer done()
	approver := modeApprover(func(context.Context, Request) (Decision, error) {
		if err := p.SetMode(Plan); err != nil {
			t.Fatal(err)
		}
		return Decision{Approved: true}, nil
	})
	if _, err := Authorize(ctx, approver, req, true); err == nil {
		t.Fatal("stale write approved after switching Plan")
	}
}

func TestProtectedApprovalIsOneOperationAndOverridesStandingAllow(t *testing.T) {
	store := newTestStore(t)
	grant := testGrant(t, ".invokr/*")
	grant.Tool = "edit"
	mustAdd(t, store, grant)
	p := NewPolicy(Auto, false)
	p.SetStore(store, nil)
	req := Request{Tool: "edit", Subject: ".invokr/config.json", Writes: true, Confined: true, Protected: true}
	count := 0
	a := modeApprover(func(context.Context, Request) (Decision, error) { count++; return Decision{Approved: true}, nil })
	for range 2 {
		ctx, done := p.Begin(context.Background(), req)
		_, err := Authorize(ctx, a, req, true)
		done()
		if err != nil {
			t.Fatal(err)
		}
	}
	if count != 2 || len(store.List()) != 1 {
		t.Fatalf("approvals=%d, stored rules=%d", count, len(store.List()))
	}
}

func TestModeConcurrencyAndRetainedWork(t *testing.T) {
	p := NewPolicy(Auto, false)
	ctx, done := p.Begin(context.Background(), Request{})
	release := Retain(ctx)
	done()
	if err := p.SetMode(Plan); err != nil {
		t.Fatal(err)
	}
	if p.PriorWork() != 1 {
		t.Fatal("background work lost")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				_ = p.SetMode(Auto)
				_ = p.SetMode(Plan)
				_ = p.Mode()
				_ = p.Revision()
				_ = p.PriorWork()
			}
		})
	}
	wg.Wait()
	release()
	release()
	if p.PriorWork() != 0 {
		t.Fatal("retained work not released")
	}
	if err := NewPolicy(YOLO, false).SetMode(Manual); err == nil {
		t.Fatal("YOLO switched without restart")
	}
}

func TestStructuredDenialPathsKeepSpaces(t *testing.T) {
	s := newTestStore(t)
	g := denial(t, "secret file")
	g.Tool = "apply_patch"
	mustAdd(t, s, g)
	d, ok := s.DenyOnly(Request{Tool: "apply_patch", Subject: "secret file public", Paths: []string{"secret file", "public"}, SubjectIsPathList: true})
	if !ok || d.Approved {
		t.Fatal("structured denied path split at whitespace")
	}
}
