package web_search

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/support/testutil"
	"github.com/invokr-agent/invokr/tools/core"
)

func TestSensitiveApprovalDenialIsAudited(t *testing.T) {
	t.Setenv(constants.BraveSearchAPIKeyEnv, "test-key")
	dispatcher := &captureDispatcher{}
	registry := &fakeRegistrar{}
	RegisterWebSearchTool(registry, core.CallerDeps{
		App:        config.AppConfig{WebSearch: config.WebSearchConfig{Providers: []string{constants.WebSearchProviderBrave}}},
		Dispatcher: dispatcher,
		Approver: testutil.StaticApprover{Decision: approval.Decision{
			Approved: false,
			Reason:   "blocked by test policy",
		}},
	})

	const query = "private key notes"
	_, err := registry.handler(context.Background(), &core.ToolCall{
		Name:      constants.WebSearchToolName,
		Arguments: json.RawMessage(`{"query":"` + query + `"}`),
	})
	if err == nil {
		t.Fatal("expected sensitive web search to be denied")
	}
	if len(dispatcher.events) != 1 || dispatcher.events[0].Audit == nil {
		t.Fatalf("expected one audit event, got %#v", dispatcher.events)
	}
	audit := dispatcher.events[0].Audit
	sensitive, ok := audit.Details["sensitive_query"].(bool)
	if audit.Outcome != "failed" || audit.Details["approval_decision"] != "denied" || !ok || !sensitive {
		t.Fatalf("unexpected denied-search audit: %#v", audit)
	}
	encoded, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), query) {
		t.Fatalf("audit leaked the raw sensitive query: %s", encoded)
	}
}

type failingApprover struct{ err error }

func (a failingApprover) RequestApproval(context.Context, approval.Request) (approval.Decision, error) {
	return approval.Decision{}, a.err
}

func TestPreExecutionFailureDoesNotInventApprovalDecision(t *testing.T) {
	t.Setenv(constants.BraveSearchAPIKeyEnv, "test-key")
	for _, tc := range []struct {
		name, args string
		approver   approval.Approver
	}{
		{"malformed", `{"query":"private key notes","max_results":"invalid"}`, nil},
		{"empty query", `{"query":""}`, nil},
		{"missing approver", `{"query":"private key notes"}`, nil},
		{"canceled approval", `{"query":"private key notes"}`, failingApprover{context.Canceled}},
		{"approval error", `{"query":"private key notes"}`, failingApprover{context.DeadlineExceeded}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher := &captureDispatcher{}
			registry := &fakeRegistrar{}
			RegisterWebSearchTool(registry, core.CallerDeps{
				App:        config.AppConfig{WebSearch: config.WebSearchConfig{Providers: []string{constants.WebSearchProviderBrave}}},
				Dispatcher: dispatcher, Approver: tc.approver,
			})
			_, err := registry.handler(context.Background(), &core.ToolCall{Name: constants.WebSearchToolName, Arguments: json.RawMessage(tc.args)})
			if err == nil || len(dispatcher.events) != 1 || dispatcher.events[0].Audit == nil {
				t.Fatalf("expected one failure audit: error %v, events %#v", err, dispatcher.events)
			}
			audit := dispatcher.events[0].Audit
			if audit.Outcome != "failed" || audit.Details["approval_decision"] != "not_approved" {
				t.Fatalf("unexpected approval decision: %#v", audit)
			}
			encoded, err := json.Marshal(audit)
			if err != nil || strings.Contains(string(encoded), "private key notes") {
				t.Fatalf("audit leaked query or failed to marshal: %s, %v", encoded, err)
			}
		})
	}
}
