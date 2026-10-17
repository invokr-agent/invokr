//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// TestC10_SkillCacheRefresh verifies that a load_skill miss rescans the skills
// directory: the first call fails (skill absent), the skill is written mid-turn
// via the provider's BeforeResponse hook, and the second call in the same
// runtime finds it (cache invalidated on miss).
func TestC10_SkillCacheRefresh(t *testing.T) {
	// The install runs mid-turn, just before the model emits its second
	// load_skill call; skillsDir is populated before the turn starts.
	var skillsDir string
	second := managedToolStep("c10-second", "load_skill", map[string]any{"name": "greet-test"})
	second.BeforeResponse = func() {
		mustWrite(t, filepath.Join(skillsDir, "greet-test", "SKILL.md"),
			"---\nname: greet-test\ndescription: test skill\n---\nReply exactly: skill-canary-73\n")
	}
	provider := providertest.NewScripted(t,
		managedToolStep("c10-first", "load_skill", map[string]any{"name": "greet-test"}),
		second,
		providertest.ReplyStep("done"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	skillsDir = home.app.Skills.Dir

	got := runInProc(t, home, provider.URL(), "load the greet-test skill", grantAll)

	if got.Resp.ToolCallCount != 4 {
		t.Fatalf("tool_call_count = %d, want 4", got.Resp.ToolCallCount)
	}
	// First call fails as unknown; second succeeds after miss-triggered rescan.
	failures := got.Resp.ToolFailures
	if len(failures) != 1 || failures[0].Tool != "load_skill" || failures[0].Kind != "error" ||
		!strings.Contains(strings.ToLower(failures[0].Detail), "unknown skill") {
		t.Fatalf("first load result = %#v, want one unknown-skill error", failures)
	}
	secondResult := tmuxResultByID(t, provider.Snapshot().CompletionRequests, "c10-second")
	if !strings.Contains(secondResult, "skill-canary-73") {
		t.Fatalf("second load result missing installed instructions: %q", secondResult)
	}
	if len(got.Approvals) != 0 {
		t.Fatalf("load_skill should not require approval: %#v", got.Approvals)
	}
}
