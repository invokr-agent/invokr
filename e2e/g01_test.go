//go:build e2e

package e2e

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/platform/runtime"
	"github.com/invokr-agent/invokr/support/testutil/providertest"
)

// writeGrants seeds the isolated home's grants file, the way pressing `g` on an
// approval (or `/grants add`) would. It goes through the real store rather than
// hand-rolling the file, so the test can never be seeded with a shape the
// product does not actually write.
func writeGrants(t *testing.T, home testHome, grants ...approval.Grant) {
	t.Helper()
	store := approval.NewStore(filepath.Join(home.state.HomeDir, "grants.json"))
	for _, g := range grants {
		if _, err := store.Add(g); err != nil {
			t.Fatalf("seed standing rule %s: %v", g.ID, err)
		}
	}
}

// runQueued drives one turn through an ApprovalQueue wired to the standing-grant
// store, mirroring cmd/agentui/run.go — the only place grants are consulted.
// Every request that survives the grant check is denied, so the subjects
// returned are exactly the calls no grant covered.
func runQueued(t *testing.T, home testHome, providerURL, prompt string) []approval.Request {
	t.Helper()
	app := home.app
	app.Provider.BaseURL = providerURL
	rt, err := runtime.Bootstrap(app, runtime.Options{})
	if err != nil {
		t.Fatalf("bootstrap in-process runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Cleanup() })
	s := rt.Services()

	queueCtx, cancelQueue := context.WithCancel(context.Background())
	defer cancelQueue()
	queue := approval.NewApprovalQueue(queueCtx, s.Approvals)
	queue.SetStandingGrants(s.Grants)

	var mu sync.Mutex
	var asked []approval.Request
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-queueCtx.Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
			for _, item := range queue.Snapshot() {
				mu.Lock()
				asked = append(asked, item.Pending.Request)
				mu.Unlock()
				queue.Decide(item.Pending.Request.ID, approval.Decision{Approved: false, Reason: "scripted deny"})
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := s.Session.Send(ctx, prompt); err != nil {
		t.Fatalf("in-process turn failed: %v", err)
	}
	cancelQueue()
	<-done
	mu.Lock()
	defer mu.Unlock()
	return append([]approval.Request(nil), asked...)
}

// TestG01_StandingGrants covers plan case G01: a standing grant on `shell`
// auto-approves the calls its pattern covers and nothing else, and the
// always-excluded tools can never be covered. Before the fix `shell` was itself
// excluded (capability-derived), so no grant could ever match.
func TestG01_StandingGrants(t *testing.T) {
	t.Run("a shell grant auto-approves only its own subject", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			shellCall("g01-granted", "git", "branch", "grant-probe"),
			shellCall("g01-other", "git", "stash"),
			providertest.ReplyStep("g01-done"),
		)
		defer provider.Close()
		home := newHome(t, provider.URL())
		writeGrants(t, home, approval.Grant{
			ID:         "g01-grant",
			Tool:       "shell",
			ArgPattern: "git branch*",
			Scope:      approval.ScopeGlobal,
			CreatedAt:  time.Now(),
			ExpiresAt:  time.Now().Add(24 * time.Hour),
		})

		// Both calls are write tier, so anything the grant does not cover must
		// reach the (denying) queue.
		asked := runQueued(t, home, provider.URL(), "run two git commands")

		if len(asked) != 1 {
			t.Fatalf("queued approvals = %#v, want only the ungranted call", asked)
		}
		if subject := asked[0].Subject; subject != "git stash" {
			t.Fatalf("approval subject = %q, want the ungranted %q", subject, "git stash")
		}
		provider.AssertClean(t)
		provider.AssertExhausted(t)
	})

	t.Run("excluded tools and invalid patterns are refused", func(t *testing.T) {
		provider := providertest.NewScripted(t, providertest.ReplyStep("g01-store-ready"))
		defer provider.Close()
		home := newHome(t, provider.URL())
		got := runInProc(t, home, provider.URL(), "boot", grantAll)
		store := got.Services.Grants
		if store == nil {
			t.Fatal("runtime exposed no standing-grant store")
		}

		valid := func(tool, pattern string) approval.Grant {
			return approval.Grant{Tool: tool, ArgPattern: pattern, Scope: approval.ScopeGlobal, ExpiresAt: time.Now().Add(time.Hour)}
		}
		// code_interpreter joins create_tool and delegate_task on the static
		// exclusion list; it used to slip through whenever its container
		// network was off.
		for _, tool := range []string{"code_interpreter", "create_tool", "delegate_task"} {
			_, err := store.Add(valid(tool, "anything-here"))
			var excluded *approval.ErrExcluded
			if !errors.As(err, &excluded) {
				t.Fatalf("Add(%q) error = %v, want ErrExcluded", tool, err)
			}
		}
		// shell, the tool standing grants exist for, is grantable.
		if _, err := store.Add(valid("shell", "git branch*")); err != nil {
			t.Fatalf("shell grant refused: %v", err)
		}
		for _, pattern := range []string{"", "*abc", "a**b", "cat ../x", "git status; rm", "*a*"} {
			_, err := store.Add(valid("shell", pattern))
			var invalid *approval.ErrInvalidPattern
			if !errors.As(err, &invalid) {
				t.Fatalf("Add(pattern %q) error = %v, want ErrInvalidPattern", pattern, err)
			}
		}
		provider.AssertClean(t)
		provider.AssertExhausted(t)
	})
}
