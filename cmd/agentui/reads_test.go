package agentui

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
)

func TestReadsCommandTogglesTheSessionSwitch(t *testing.T) {
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	m.setReads([]string{"ask"})
	if !m.policy.OutsideReads() {
		t.Fatal("/reads ask left outside reads off")
	}
	m.setReads([]string{"workspace"})
	if m.policy.OutsideReads() {
		t.Fatal("/reads workspace left outside reads on")
	}
}

// "a" on an outside read grants the directory the tool proposed, never a
// pattern derived from the path list, and nothing when it proposed none.
func TestOutsideReadGrantPattern(t *testing.T) {
	req := approval.Request{Tool: "outside_read", Outside: true, Subject: "/src/x/a.md", GrantPattern: "/src/x/*"}
	if got, err := grantPattern(req); err != nil || got != "/src/x/*" {
		t.Fatalf("pattern = %q, %v", got, err)
	}
	req.GrantPattern = ""
	if _, err := grantPattern(req); err == nil || !strings.Contains(err.Error(), "too broad") {
		t.Fatalf("broad scope: err = %v", err)
	}
}
