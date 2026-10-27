//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

func patchCall(id, patch string) providertest.CompletionStep {
	return providertest.NativeToolCallStep(id, "apply_patch", map[string]any{"patch": patch})
}

// TestP01_ApplyPatchAmbiguityRejected covers plan case P01: a hunk whose
// context matches more than one location is rejected at prepare time with a
// disambiguation error, and the file is left byte-identical. Before #274 this
// input applied silently at the first match.
func TestP01_ApplyPatchAmbiguityRejected(t *testing.T) {
	t.Run("two matching locations are rejected", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			patchCall("p01-dup", "*** Begin Patch\n*** Update File: dup.txt\n@@\n dup\n+added\n*** End Patch"),
			providertest.ReplyStep("p01-done"),
		)
		defer provider.Close()
		home := newHome(t, provider.URL())
		path := filepath.Join(home.workspace, "dup.txt")
		mustWrite(t, path, "dup\nmiddle\ndup\n")
		before := fileSHA256(t, path)

		got := runInProc(t, home, provider.URL(), "insert after dup", grantAll)

		const want = "hunk 1: context matches 2 locations (first at lines 1 and 3); add more context lines to disambiguate"
		results := toolResults(provider.Snapshot().CompletionRequests)
		if !strings.Contains(results, want) {
			t.Fatalf("ambiguity error not reported verbatim; tool results:\n%s", results)
		}
		if after := fileSHA256(t, path); after != before {
			t.Fatalf("rejected patch mutated %s", path)
		}
		if len(got.Resp.ToolFailures) != 1 || got.Resp.ToolFailures[0].Tool != "apply_patch" {
			t.Fatalf("ledger = %#v, want one apply_patch failure", got.Resp.ToolFailures)
		}
		provider.AssertClean(t)
		provider.AssertExhausted(t)
	})

	t.Run("match count caps at the enumeration limit", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			patchCall("p01-many", "*** Begin Patch\n*** Update File: many.txt\n@@\n dup\n+added\n*** End Patch"),
			providertest.ReplyStep("p01-many-done"),
		)
		defer provider.Close()
		home := newHome(t, provider.URL())
		path := filepath.Join(home.workspace, "many.txt")
		mustWrite(t, path, strings.Repeat("dup\nfiller\n", 6))
		before := fileSHA256(t, path)

		runInProc(t, home, provider.URL(), "insert after dup", grantAll)

		// hunkMatchLimit stops enumeration at 4; the message shape is unchanged.
		const want = "hunk 1: context matches 4 locations (first at lines 1 and 3); add more context lines to disambiguate"
		results := toolResults(provider.Snapshot().CompletionRequests)
		if !strings.Contains(results, want) {
			t.Fatalf("capped ambiguity error not reported verbatim; tool results:\n%s", results)
		}
		if after := fileSHA256(t, path); after != before {
			t.Fatalf("rejected patch mutated %s", path)
		}
		provider.AssertClean(t)
		provider.AssertExhausted(t)
	})
}
