//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// patchFailureDetail returns the verbatim diagnostic the apply_patch failure
// recorded in the outcome ledger — the same text that reaches the model as the
// tool result, before the tool-result payload's JSON escaping.
func patchFailureDetail(t *testing.T, got inProcResult) string {
	t.Helper()
	for _, failure := range got.Resp.ToolFailures {
		if failure.Tool == "apply_patch" {
			return failure.Detail
		}
	}
	t.Fatalf("ledger has no apply_patch failure: %#v", got.Resp.ToolFailures)
	return ""
}

// tenLineFixture is the P02 fixture: ten distinct, 1-based-countable lines.
const tenLineFixture = "line01\nline02\nline03\nline04\nline05\nline06\nline07\nline08\nline09\nline10\n"

// TestP02_ApplyPatchMismatchDiagnostics covers plan case P02: each of the four
// hunk-mismatch shapes reports its own diagnostic verbatim, and every rejected
// patch leaves the file byte-identical (rejection happens at prepare time).
func TestP02_ApplyPatchMismatchDiagnostics(t *testing.T) {
	cases := []struct {
		name     string
		file     string
		content  string
		patch    string
		want     string
		wantAlso []string
	}{
		{
			name:    "out of order hunks",
			file:    "order.txt",
			content: tenLineFixture,
			patch: "*** Begin Patch\n*** Update File: order.txt\n@@\n line05\n+inserted\n" +
				"@@\n line02\n+too-early\n*** End Patch",
			want: "hunk 2: context matches at line 2, before the previous hunk ended at line 6; hunks must be ordered by file position",
		},
		{
			name:    "context absent entirely",
			file:    "absent.txt",
			content: tenLineFixture,
			patch:   "*** Begin Patch\n*** Update File: absent.txt\n@@\n nothing-here\n+inserted\n*** End Patch",
			want:    `hunk 1: context not found in lines 1-10; no line matches "nothing-here"`,
		},
		{
			name:    "partial match runs off the end of the file",
			file:    "eof.txt",
			content: tenLineFixture,
			patch:   "*** Begin Patch\n*** Update File: eof.txt\n@@\n line09\n line10\n line11\n+inserted\n*** End Patch",
			want:    "hunk 1: context not found; matched 2 of 3 lines at line 9, then the file ended",
		},
		{
			name:    "divergence reports expected and actual",
			file:    "diverge.txt",
			content: tenLineFixture,
			patch:   "*** Begin Patch\n*** Update File: diverge.txt\n@@\n line03\n lineXX\n+inserted\n*** End Patch",
			want:    "hunk 1: context not found; closest match at line 3 matched 1 of 2 lines, then diverged",
			wantAlso: []string{
				`expected: "lineXX"`,
				`actual:   "line04"`,
			},
		},
		// A whitespace-only divergence that matches one place applies (the
		// fuzzy fallback); it is only reported when the loose match is
		// ambiguous, and then the hint says what the difference was.
		{
			name:    "ambiguous whitespace-only divergence is called out",
			file:    "ws.txt",
			content: "start\n\tindented\nend\nstart\n  indented\nend\n",
			patch:   "*** Begin Patch\n*** Update File: ws.txt\n@@\n start\n     indented\n+inserted\n*** End Patch",
			want:    "hunk 1: context not found; closest match at line 1 matched 1 of 2 lines, then diverged (whitespace/indentation only)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := providertest.NewScripted(t,
				patchCall("p02-"+tc.file, tc.patch),
				providertest.ReplyStep("p02-done"),
			)
			defer provider.Close()
			home := newHome(t, provider.URL())
			path := filepath.Join(home.workspace, tc.file)
			mustWrite(t, path, tc.content)
			before := fileSHA256(t, path)

			got := runInProc(t, home, provider.URL(), "apply the patch", grantAll)

			detail := patchFailureDetail(t, got)
			for _, want := range append([]string{tc.want}, tc.wantAlso...) {
				if !strings.Contains(detail, want) {
					t.Fatalf("diagnostic %q not reported verbatim; ledger detail:\n%s", want, detail)
				}
			}
			if after := fileSHA256(t, path); after != before {
				t.Fatalf("rejected patch mutated %s", path)
			}
			provider.AssertClean(t)
			provider.AssertExhausted(t)
		})
	}
}
