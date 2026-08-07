package apply_patch

import (
	"strings"
	"testing"
)

func hunkOf(kinds string, texts ...string) patchHunk {
	hunk := patchHunk{}
	for i, text := range texts {
		hunk.Lines = append(hunk.Lines, patchLine{Kind: kinds[i], Text: text})
	}
	return hunk
}

func mustApplyHunksError(t *testing.T, original string, hunks []patchHunk) string {
	t.Helper()
	if _, err := applyHunks(original, hunks); err != nil {
		return err.Error()
	}
	t.Fatal("expected applyHunks to fail")
	return ""
}

func TestFindHunkMatchesReportsEveryOccurrence(t *testing.T) {
	lines := []string{"a", "b", "a", "b", "c"}
	got := findHunkMatches(lines, 0, []string{"a", "b"}, exactLineMatch)
	if len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("findHunkMatches = %v, want [0 2]", got)
	}
}

func TestFindHunkMatchesStopsAtLimit(t *testing.T) {
	lines := strings.Split(strings.Repeat("x\n", 10), "\n")
	if got := findHunkMatches(lines, 0, []string{"x"}, exactLineMatch); len(got) != hunkMatchLimit {
		t.Fatalf("findHunkMatches returned %d matches, want %d", len(got), hunkMatchLimit)
	}
}

func TestFindHunkMatchesEmptySequenceAnchorsAtCursor(t *testing.T) {
	got := findHunkMatches([]string{"a", "b"}, 1, nil, exactLineMatch)
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("findHunkMatches = %v, want [1]", got)
	}
}

func TestApplyHunksEmptyHunkLeavesFileUnchanged(t *testing.T) {
	out, err := applyHunks("a\nb\n", []patchHunk{{}})
	if err != nil {
		t.Fatalf("applyHunks: %v", err)
	}
	if out != "a\nb\n" {
		t.Fatalf("applyHunks = %q, want unchanged", out)
	}
}

func TestApplyHunksAmbiguousContextIsRejected(t *testing.T) {
	msg := mustApplyHunksError(t, "dup\nx\ndup\ny\ndup\n", []patchHunk{hunkOf(" +", "dup", "added")})
	for _, want := range []string{"hunk 1", "matches 3 locations", "first at lines 1 and 3"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
}

func TestApplyHunksMismatchNamesClosestCandidateAndDivergingLines(t *testing.T) {
	msg := mustApplyHunksError(t, "alpha\nbeta\ngamma\n", []patchHunk{hunkOf("  -", "alpha", "beta", "delta")})
	for _, want := range []string{"closest match at line 1", "matched 2 of 3 lines", `expected: "delta"`, `actual:   "gamma"`} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
}

// A context sequence that differs from the file only in whitespace applies via
// the fuzzy fallback when the match is unique; deleted lines are consumed from
// the file, so the model's wrong indentation never lands in the output.
func TestApplyHunksWhitespaceOnlyDifferenceAppliesViaFuzzyMatch(t *testing.T) {
	out, err := applyHunks("func f() {\n\tx := 1\n}\n", []patchHunk{hunkOf(" -+", "func f() {", "    x := 1", "\ty := 2")})
	if err != nil {
		t.Fatalf("applyHunks: %v", err)
	}
	if out != "func f() {\n\ty := 2\n}\n" {
		t.Fatalf("applyHunks = %q", out)
	}
}

// A trailing-whitespace "blank" line in the file matches an empty context line
// (observed: a fixture with a four-space line no model could quote).
func TestApplyHunksBlankContextMatchesTrailingWhitespaceLine(t *testing.T) {
	out, err := applyHunks("a\n    \nb\n", []patchHunk{hunkOf("  +", "", "b", "c")})
	if err != nil {
		t.Fatalf("applyHunks: %v", err)
	}
	if out != "a\n    \nb\nc\n" {
		t.Fatalf("applyHunks = %q", out)
	}
}

// The fuzzy fallback never guesses between two candidates.
func TestApplyHunksFuzzyMatchAmbiguityStillRejected(t *testing.T) {
	original := "if a:\n    x = 1\nelse:\n  x = 1\n"
	msg := mustApplyHunksError(t, original, []patchHunk{hunkOf("-", "x = 1")})
	if !strings.Contains(msg, "context not found") {
		t.Fatalf("error %q should reject ambiguous fuzzy match", msg)
	}
}

// An exact match wins even when fuzzy candidates exist elsewhere.
func TestApplyHunksExactMatchPreferredOverFuzzy(t *testing.T) {
	out, err := applyHunks("  x = 1\nx = 1\n", []patchHunk{hunkOf("-+", "x = 1", "x = 2")})
	if err != nil {
		t.Fatalf("applyHunks: %v", err)
	}
	if out != "  x = 1\nx = 2\n" {
		t.Fatalf("applyHunks = %q", out)
	}
}

func TestApplyHunksMismatchReportsOutOfOrderHunk(t *testing.T) {
	hunks := []patchHunk{hunkOf(" +", "three", "added"), hunkOf(" +", "one", "also")}
	msg := mustApplyHunksError(t, "one\ntwo\nthree\n", hunks)
	for _, want := range []string{"hunk 2", "hunks must be ordered by file position"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
}

func TestApplyHunksMismatchWithNoPartialMatchNamesSearchWindow(t *testing.T) {
	msg := mustApplyHunksError(t, "a\nb\nc\n", []patchHunk{hunkOf("-", "zzz")})
	if !strings.Contains(msg, "context not found in lines 1-3") || !strings.Contains(msg, `"zzz"`) {
		t.Fatalf("error %q missing search window", msg)
	}
}

func TestApplyHunksMismatchAtEndOfFileReportsTruncation(t *testing.T) {
	msg := mustApplyHunksError(t, "a\nb\n", []patchHunk{hunkOf("   ", "a", "b", "c")})
	if !strings.Contains(msg, "the file ended") {
		t.Fatalf("error %q missing end-of-file diagnosis", msg)
	}
}

func TestApplyHunksPreservesMissingTrailingNewline(t *testing.T) {
	out, err := applyHunks("a\nb", []patchHunk{hunkOf(" +", "a", "mid")})
	if err != nil {
		t.Fatalf("applyHunks: %v", err)
	}
	if out != "a\nmid\nb" {
		t.Fatalf("applyHunks = %q, want %q", out, "a\nmid\nb")
	}
}

// A file with one stray CRLF line must not be rewritten end to end: converting
// every ending turned a one-line edit into a whole-file diff, and disagreed
// with edit, which decides by the dominant ending and touches only the
// region it replaced.
func TestApplyHunksKeepsEachLinesOwnEnding(t *testing.T) {
	original := "alpha\nbeta\ngamma\r\ndelta\n"
	hunk := patchHunk{Lines: []patchLine{
		{Kind: '-', Text: "alpha"},
		{Kind: '+', Text: "ALPHA"},
	}}
	got, err := applyHunks(original, []patchHunk{hunk})
	if err != nil {
		t.Fatalf("applyHunks: %v", err)
	}
	if want := "ALPHA\nbeta\ngamma\r\ndelta\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// In a CRLF file the context copied out of read_file carries carriage returns
// the normalized patch text does not, and an added line has to take the
// file's ending rather than a bare newline.
func TestApplyHunksAddsTheFilesEndingInACRLFFile(t *testing.T) {
	original := "alpha\r\nbeta\r\ngamma\r\n"
	hunk := patchHunk{Lines: []patchLine{
		{Kind: ' ', Text: "alpha"},
		{Kind: '-', Text: "beta"},
		{Kind: '+', Text: "BETA"},
		{Kind: '+', Text: "EXTRA"},
	}}
	got, err := applyHunks(original, []patchHunk{hunk})
	if err != nil {
		t.Fatalf("applyHunks: %v", err)
	}
	if want := "alpha\r\nBETA\r\nEXTRA\r\ngamma\r\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
