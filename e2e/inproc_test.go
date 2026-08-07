//go:build e2e

package e2e

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/runtime"
	"github.com/invokr-ai/invokr/platform/turn"
	toolcore "github.com/invokr-ai/invokr/tools/core"
)

// approveFunc decides a single approval request. Returning a denied decision
// surfaces as an approval.DeniedError to the tool, exactly like a user denial.
type approveFunc func(approval.Request) approval.Decision

// grantAll and denyAll are the common policies; case-specific policies inspect
// the request's Tool/Details/Summary.
func grantAll(approval.Request) approval.Decision { return approval.Decision{Approved: true} }

func denyAll(approval.Request) approval.Decision {
	return approval.Decision{Approved: false, Reason: "scripted deny"}
}

type toolEvent struct {
	Name  string
	Type  events.EventType
	Code  string // completed-but-flagged marker (e.g. "denied", "exit 1")
	Error string
	// Progress carries an incremental status line (EventToolProgress).
	Progress string
}

type inProcResult struct {
	Resp      turn.AgentResponse
	Tools     []toolEvent
	Approvals []approval.Request
	Notices   []string
	Services  runtime.Services
}

// hasNotice reports whether any notice event contained substring text.
func (r inProcResult) hasNotice(text string) bool {
	for _, notice := range r.Notices {
		if strings.Contains(notice, text) {
			return true
		}
	}
	return false
}

// completedKinds returns the tool_failures kinds keyed by tool, mirroring the
// exec ledger's authoritative outcome ledger.
func (r inProcResult) failureKinds() []struct{ Tool, Kind string } {
	var out []struct{ Tool, Kind string }
	for _, f := range r.Resp.ToolFailures {
		out = append(out, struct{ Tool, Kind string }{f.Tool, f.Kind})
	}
	return out
}

// runInProc boots a runtime against the given fake provider, drives one turn
// with a scripted approver, and returns the response plus the ordered tool
// events and the approval requests that were raised. The runtime is torn down
// on test cleanup.
func runInProc(t *testing.T, home testHome, providerURL, prompt string, approve approveFunc) inProcResult {
	return runInProcWhen(t, home, providerURL, prompt, approve, nil)
}

func runInProcWhen(t *testing.T, home testHome, providerURL, prompt string, approve approveFunc, ready func(runtime.Services)) inProcResult {
	t.Helper()
	app := home.app
	app.Provider.BaseURL = providerURL

	rt, err := runtime.Bootstrap(app, runtime.Options{})
	if err != nil {
		t.Fatalf("bootstrap in-process runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Cleanup() })
	s := rt.Services()
	if ready != nil {
		ready(s)
	}

	var mu sync.Mutex
	var tools []toolEvent
	var notices []string
	stop := s.Events.Register(events.HandlerFunc(func(_ context.Context, ev events.Event) error {
		if ev.Type == events.EventNotice && ev.Notice != nil {
			mu.Lock()
			notices = append(notices, ev.Notice.Text)
			mu.Unlock()
			return nil
		}
		if ev.Tool == nil {
			return nil
		}
		switch ev.Type {
		case events.EventToolStarted, events.EventToolCompleted, events.EventToolFailed, events.EventToolProgress:
			mu.Lock()
			tools = append(tools, toolEvent{Name: ev.Tool.Name, Type: ev.Type, Code: ev.Tool.Code, Error: ev.Tool.Error, Progress: ev.Tool.Progress})
			mu.Unlock()
		}
		return nil
	}))
	defer stop()

	// Drive the approval broker with the scripted policy until teardown.
	var approvals []approval.Request
	apprCtx, cancelAppr := context.WithCancel(context.Background())
	defer cancelAppr()
	go func() {
		for {
			select {
			case <-apprCtx.Done():
				return
			case pending, ok := <-s.Approvals.Requests():
				if !ok {
					return
				}
				mu.Lock()
				approvals = append(approvals, pending.Request)
				mu.Unlock()
				pending.Decide(approve(pending.Request))
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := s.Session.Send(ctx, prompt)
	if err != nil {
		t.Fatalf("in-process turn failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	return inProcResult{
		Resp:      resp,
		Tools:     append([]toolEvent(nil), tools...),
		Approvals: append([]approval.Request(nil), approvals...),
		Notices:   append([]string(nil), notices...),
		Services:  s,
	}
}

func waitForSessionTool(t *testing.T, services runtime.Services, name string) {
	t.Helper()
	source, ok := services.Session.(interface{ ToolSpecs() []toolcore.ToolSpec })
	if !ok {
		t.Fatal("session does not expose tool specs")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, spec := range source.ToolSpecs() {
			if spec.Name == name {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("tool %q was not registered", name)
}
