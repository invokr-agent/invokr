package agentui

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
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

// A push names its own grant (one remote and branch) and is grantable though
// it publishes; a force push carries none and stays one-time.
func TestPushGrantPattern(t *testing.T) {
	req := approval.Request{Tool: "git_write", Publishes: true, PolicyChecked: true, Subject: "push origin feat", GrantPattern: "push origin feat"}
	if got, err := grantPattern(req); err != nil || got != "push origin feat" {
		t.Fatalf("pattern = %q, %v", got, err)
	}
	req.GrantPattern, req.Subject = "", "push --force-with-lease origin feat"
	if _, err := grantPattern(req); err == nil || !strings.Contains(err.Error(), "one-time") {
		t.Fatalf("force push: err = %v", err)
	}
}
