package edit_file

import (
	"strings"
	"testing"
)

const anchorFile = "package p\n\nfunc run(a int) error {\n\tcheck(a)\n\tlog(a)\n\treturn nil\n}\n\nfunc other() {}\n"

func TestBlockAnchorRescuesDriftedInterior(t *testing.T) {
	old := "func run(a int) error {\n\tcheck(a)\n\tlog(a)\n\treturn nil\n}"
	_, n, err := replaceIn(anchorFile, old+"\n", "func run(a int) error { return nil }\n", false, "\n")
	if err != nil || n != 1 {
		t.Fatalf("exact old should still match on rung 1: n=%d err=%v", n, err)
	}
	drifted := "func run(a int) error {\n\tcheck(a)\n\tlogf(a)\n\treturn nil\n}"
	got, n, err := replaceIn(anchorFile, drifted, "func run(a int) error { return nil }", false, "\n")
	if err != nil || n != 1 {
		t.Fatalf("block anchor should rescue a drifted interior: n=%d err=%v", n, err)
	}
	if want := "package p\n\nfunc run(a int) error { return nil }\n\nfunc other() {}\n"; got != want {
		t.Fatalf("wrong region written:\n%q\nwant\n%q", got, want)
	}
}

func TestBlockAnchorRefusesRewrittenInterior(t *testing.T) {
	old := "func run(a int) error {\n\tsomethingElse()\n\tandMore()\n\tyetMore()\n}"
	if _, _, err := replaceIn(anchorFile, old, "x", false, "\n"); err != errOldNotFound {
		t.Fatalf("interior below the similarity floor must not match: %v", err)
	}
}

func TestBlockAnchorRefusesAmbiguousAndReplaceAll(t *testing.T) {
	twice := anchorFile + "\nfunc run(a int) error {\n\tcheck(a)\n\tlog(a)\n\treturn nil\n}\n"
	drifted := "func run(a int) error {\n\tcheck(a)\n\tlogf(a)\n\treturn nil\n}"
	if _, _, err := replaceIn(twice, drifted, "x", false, "\n"); err != errOldNotFound {
		t.Fatalf("two anchor candidates must refuse, got %v", err)
	}
	if _, _, err := replaceIn(anchorFile, drifted, "x", true, "\n"); err != errOldNotFound {
		t.Fatalf("replace_all must not reach the block-anchor rung, got %v", err)
	}
}

func TestBlockAnchorNeedsThreeLines(t *testing.T) {
	if _, _, err := replaceIn(anchorFile, "func run(a int) error {\n}", "x", false, "\n"); err != errOldNotFound {
		t.Fatalf("a two-line old must not anchor, got %v", err)
	}
}

// A stale old whose middle the file has since grown past: same anchors, more
// interior. Whether that rescues depends on how much of the block the new line
// is. In a long block one added line stays above the similarity floor; in a
// short one it does not, which is the coverage 0.85 gives up against opencode's
// 0.65 (#577).
func TestBlockAnchorStaleMiddleGrew(t *testing.T) {
	body := strings.Repeat("\tstep(a)\n", 12)
	grown := "func run(a int) error {\n" + body + "\taudit(a)\n\treturn nil\n}\n"
	stale := "func run(a int) error {\n" + body + "\treturn nil\n}"
	got, n, err := replaceIn(grown, stale, "func run(a int) error { return nil }", false, "\n")
	if err != nil || n != 1 {
		t.Fatalf("one extra line in a long block is within the band: n=%d err=%v", n, err)
	}
	if want := "func run(a int) error { return nil }\n"; got != want {
		t.Fatalf("wrong region written:\n%q\nwant\n%q", got, want)
	}
	short := "package p\n\nfunc run(a int) error {\n\tcheck(a)\n\tlog(a)\n\taudit(a)\n\treturn nil\n}\n"
	staleShort := "func run(a int) error {\n\tcheck(a)\n\tlog(a)\n\treturn nil\n}"
	if _, _, err := replaceIn(short, staleShort, "x", false, "\n"); err != errOldNotFound {
		t.Fatalf("at 0.85 an added line in a 3-line interior scores 0.63 and must refuse, got %v", err)
	}
	var big strings.Builder
	big.WriteString("func run(a int) error {\n\tcheck(a)\n")
	for range 20 {
		big.WriteString("\taudit(a)\n")
	}
	big.WriteString("\treturn nil\n}\n")
	if _, _, err := replaceIn(big.String(), staleShort, "x", false, "\n"); err != errOldNotFound {
		t.Fatalf("a block that grew past the line band must refuse, got %v", err)
	}
}

// The rung matches whole lines, so the replacement must come back carrying the
// file's own terminator rather than the bare newlines the model sent. The
// fixture is long enough that one drifted line stays above the 0.85 floor —
// at three lines it does not, which is the rung's short-block cost, not an
// EOL failure (#577).
func TestBlockAnchorKeepsCRLF(t *testing.T) {
	body := strings.Repeat("\tstep(a)\n", 12)
	file := "package p\n\nfunc run(a int) error {\n" + body + "\tlog(a)\n\treturn nil\n}\n\nfunc other() {}\n"
	drifted := "func run(a int) error {\n" + body + "\tlogf(a)\n\treturn nil\n}"
	crlf := strings.ReplaceAll(file, "\n", "\r\n")
	got, n, err := replaceIn(crlf, drifted, "func run(a int) error { return nil }", false, "\r\n")
	if err != nil || n != 1 {
		t.Fatalf("block anchor should match across line endings: n=%d err=%v", n, err)
	}
	want := strings.ReplaceAll("package p\n\nfunc run(a int) error { return nil }\n\nfunc other() {}\n", "\n", "\r\n")
	if got != want {
		t.Fatalf("line endings not preserved:\n%q\nwant\n%q", got, want)
	}
}

// Line count alone is not enough of a guard: a candidate can sit inside the
// band and still be far larger than old, which is the shape that edits the
// wrong region. blockAnchorMaxGrowth catches it on bytes.
func TestBlockAnchorRefusesDisproportionateMatch(t *testing.T) {
	huge := "func run(a int) error {\n\tcheck(a)\n\t" + strings.Repeat("x", 4096) + "\n\treturn nil\n}\n"
	stale := "func run(a int) error {\n\tcheck(a)\n\tlog(a)\n\treturn nil\n}"
	if _, _, err := replaceIn(huge, stale, "x", false, "\n"); err != errOldNotFound {
		t.Fatalf("a candidate far larger than old must refuse, got %v", err)
	}
}

// The last line is not an anchor: an old whose closing line drifted still
// lands on the block its first line and interior identify.
func TestBlockAnchorLastLineDrifted(t *testing.T) {
	drifted := "func run(a int) error {\n\tcheck(a)\n\tlog(a)\n\treturn nil\n};"
	got, n, err := replaceIn(anchorFile, drifted, "func run(a int) error { return nil }", false, "\n")
	if err != nil || n != 1 {
		t.Fatalf("a drifted last line should still match: n=%d err=%v", n, err)
	}
	if want := "package p\n\nfunc run(a int) error { return nil }\n\nfunc other() {}\n"; got != want {
		t.Fatalf("wrong region written:\n%q\nwant\n%q", got, want)
	}
}
