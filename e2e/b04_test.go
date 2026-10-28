//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/support/testutil/providertest"
)

const repeatNotice = "repeated tool call detected; asking model to change approach"

// searchCall is a successful, cheap tool call whose signature is fully
// determined by the query, so a script can repeat or vary it at will.
func searchCall(id, query string) providertest.CompletionStep {
	return providertest.NativeToolCallStep(id, "tools", map[string]any{"action": "search", "query": query})
}

// TestB04_RepeatGuardNonConsecutive covers plan cases B04/P03: the guard counts
// each tool-call signature across the whole request, so three byte-identical
// calls separated by other turns trip it. Before the fix only consecutive
// repeats counted and this scenario nudged nothing.
func TestB04_RepeatGuardNonConsecutive(t *testing.T) {
	t.Run("identical calls separated by other turns trip the guard", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			searchCall("b04-1", "zzz"),
			searchCall("b04-2", "yyy"),
			searchCall("b04-3", "zzz"),
			searchCall("b04-4", "xxx"),
			searchCall("b04-5", "zzz"),
			providertest.ReplyStep("b04-done"),
		)
		defer provider.Close()
		home := newHome(t, provider.URL())

		got := runInProc(t, home, provider.URL(), "search repeatedly", grantAll)

		if !got.hasNotice(repeatNotice) {
			t.Fatalf("notices = %v, want %q", got.Notices, repeatNotice)
		}
		// The nudge is an internal message to the model; it must never reach
		// the user's transcript.
		if strings.Contains(got.Resp.Message, "[Continuing your task above]") {
			t.Fatalf("internal nudge leaked into the answer: %q", got.Resp.Message)
		}
		if got.Resp.Message != "b04-done" {
			t.Fatalf("message = %q, want b04-done", got.Resp.Message)
		}
		provider.AssertClean(t)
		provider.AssertExhausted(t)
	})

	t.Run("alternating calls trip the guard", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			searchCall("b04-a1", "aaa"),
			searchCall("b04-b1", "bbb"),
			searchCall("b04-a2", "aaa"),
			searchCall("b04-b2", "bbb"),
			providertest.ReplyStep("b04-alt-done"),
		)
		defer provider.Close()
		home := newHome(t, provider.URL())

		got := runInProc(t, home, provider.URL(), "alternate searches", grantAll)

		if !got.hasNotice(repeatNotice) {
			t.Fatalf("notices = %v, want %q", got.Notices, repeatNotice)
		}
		provider.AssertClean(t)
		provider.AssertExhausted(t)
	})

	t.Run("varied calls never trip the guard", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			searchCall("b04-v1", "one"),
			searchCall("b04-v2", "two"),
			searchCall("b04-v3", "three"),
			searchCall("b04-v4", "four"),
			providertest.ReplyStep("b04-varied-done"),
		)
		defer provider.Close()
		home := newHome(t, provider.URL())

		got := runInProc(t, home, provider.URL(), "search four things", grantAll)

		if got.hasNotice(repeatNotice) {
			t.Fatalf("guard fired on four distinct calls: %v", got.Notices)
		}
		provider.AssertClean(t)
		provider.AssertExhausted(t)
	})
}
