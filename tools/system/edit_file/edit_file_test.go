package edit_file

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

func testPolicy(t *testing.T) (workspace.Policy, string) {
	t.Helper()
	root := t.TempDir()
	return workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root}), root
}

func write(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func plan(t *testing.T, wsp workspace.Policy, args editArgs) (preparedEdit, error) {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return prepareEdit(&core.ToolCall{Name: "edit", Arguments: data}, wsp)
}

func TestReplaceInExactUnique(t *testing.T) {
	got, n, err := replaceIn("a\nold\nb\n", "old", "new", false, "\n")
	if err != nil {
		t.Fatalf("replaceIn: %v", err)
	}
	if got != "a\nnew\nb\n" || n != 1 {
		t.Fatalf("got %q n=%d", got, n)
	}
}

func TestReplaceInAmbiguousWithoutReplaceAll(t *testing.T) {
	_, _, err := replaceIn("x\nx\n", "x", "y", false, "\n")
	if err == nil || !strings.Contains(err.Error(), "matches 2 locations") {
		t.Fatalf("err = %v", err)
	}
}

func TestReplaceInReplaceAll(t *testing.T) {
	got, n, err := replaceIn("x\nx\n", "x", "y", true, "\n")
	if err != nil {
		t.Fatalf("replaceIn: %v", err)
	}
	if got != "y\ny\n" || n != 2 {
		t.Fatalf("got %q n=%d", got, n)
	}
}

func TestReplaceInLadderTrailingWhitespace(t *testing.T) {
	got, _, err := replaceIn("keep\nvalue\ntail\n", "value   ", "fresh", false, "\n")
	if err != nil {
		t.Fatalf("replaceIn: %v", err)
	}
	if got != "keep\nfresh\ntail\n" {
		t.Fatalf("got %q", got)
	}
}

func TestReplaceInLadderIndentation(t *testing.T) {
	got, _, err := replaceIn("func f() {\n\treturn 1\n}\n", "  return 1", "  return 2", false, "\n")
	if err != nil {
		t.Fatalf("replaceIn: %v", err)
	}
	if got != "func f() {\n  return 2\n}\n" {
		t.Fatalf("got %q", got)
	}
}

func TestReplaceInNotFoundSuggestsClosestLine(t *testing.T) {
	err := notFoundError("alpha beta\ngamma\n", "alpha bota")
	if err == nil || !strings.Contains(err.Error(), "closest match in the file:\nalpha beta\n") || !strings.Contains(err.Error(), `old line 1: you gave "alpha bota", the file has "alpha beta"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestReplaceInNotFoundQuotesMultiLineContext(t *testing.T) {
	err := notFoundError("alpha\nbeta\ngamma\n", "alpha\nbota")
	if err == nil || !strings.Contains(err.Error(), "closest match in the file:\nalpha\nbeta\n") || !strings.Contains(err.Error(), `old line 2: you gave "bota", the file has "beta"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestReplaceInNotFoundNoResemblance(t *testing.T) {
	err := notFoundError("alpha\n", "zeta")
	if err == nil || !strings.Contains(err.Error(), "nothing resembles") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareEditCreatesMissingFile(t *testing.T) {
	wsp, root := testPolicy(t)
	p, err := plan(t, wsp, editArgs{Path: "new/file.txt", Edits: []editOp{{New: "hello\n"}}})
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	if !p.created || p.updated != "hello\n" {
		t.Fatalf("plan = %+v", p)
	}
	if _, err := runPreparedEdit(t.Context(), nil, p); err != nil {
		t.Fatalf("runPreparedEdit: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "new/file.txt"))
	if err != nil || string(content) != "hello\n" {
		t.Fatalf("content = %q err = %v", content, err)
	}
}

func TestPrepareEditEmptyOldOverwritesExistingFile(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.txt", "body\n")
	p, err := plan(t, wsp, editArgs{Path: "a.txt", Edits: []editOp{{New: "fresh\n"}}})
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	if !p.overwrote || p.created || p.updated != "fresh\n" {
		t.Fatalf("plan = %+v", p)
	}
	if _, err := runPreparedEdit(t.Context(), nil, p); err != nil {
		t.Fatalf("runPreparedEdit: %v", err)
	}
	content, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(content) != "fresh\n" {
		t.Fatalf("content = %q", content)
	}
}

func TestPrepareEditRejectsEmptyOldAndNewOnExistingFile(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.txt", "body\n")
	if _, err := plan(t, wsp, editArgs{Path: "a.txt"}); err == nil {
		t.Fatal("expected error for empty old and new on existing file")
	}
}

func TestPrepareEditMissingFileWithOldCreatesFromNew(t *testing.T) {
	wsp, root := testPolicy(t)
	p, err := plan(t, wsp, editArgs{Path: "new/gone.txt", Edits: []editOp{{Old: "a", New: "b\n"}}})
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	if !p.created || p.updated != "b\n" || !strings.Contains(p.note, "old was ignored") {
		t.Fatalf("plan = %+v", p)
	}
	out, err := runPreparedEdit(t.Context(), nil, p)
	if err != nil {
		t.Fatalf("runPreparedEdit: %v", err)
	}
	if !strings.Contains(out, `"action":"create"`) || !strings.Contains(out, "did not exist, so it was created with new as its whole content; old was ignored") {
		t.Fatalf("result = %s", out)
	}
	content, err := os.ReadFile(filepath.Join(root, "new/gone.txt"))
	if err != nil || string(content) != "b\n" {
		t.Fatalf("content = %q err = %v", content, err)
	}
}

func TestPrepareEditMissingFileWithOldDryRunDoesNotWrite(t *testing.T) {
	wsp, root := testPolicy(t)
	p, err := plan(t, wsp, editArgs{Path: "gone.txt", DryRun: true, Edits: []editOp{{Old: "a", New: "b"}}})
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	out, err := runPreparedEdit(t.Context(), nil, p)
	if err != nil {
		t.Fatalf("runPreparedEdit: %v", err)
	}
	if !strings.Contains(out, `"dry_run":true`) || !strings.Contains(out, "old was ignored") {
		t.Fatalf("result = %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("dry run must not create the file, stat err = %v", err)
	}
}

func TestEditFileWritesReplacement(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.txt", "one\ntwo\n")
	p, err := plan(t, wsp, editArgs{Path: "a.txt", Edits: []editOp{{Old: "two", New: "three"}}})
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	out, err := runPreparedEdit(t.Context(), nil, p)
	if err != nil {
		t.Fatalf("runPreparedEdit: %v", err)
	}
	if !strings.Contains(out, "\"replaced\":1") {
		t.Fatalf("result = %s", out)
	}
	content, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(content) != "one\nthree\n" {
		t.Fatalf("content = %q", content)
	}
}

func TestPrepareEditRejectsEscapingPath(t *testing.T) {
	wsp, _ := testPolicy(t)
	if _, err := plan(t, wsp, editArgs{Path: "../outside.txt", Edits: []editOp{{Old: "a", New: "b"}}}); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}

func TestEditsBatchAppliesInOrder(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.txt", "one\ntwo\nthree\n")
	p, err := plan(t, wsp, editArgs{Path: "a.txt", Edits: []editOp{
		{Old: "one", New: "uno"},
		{Old: "uno\ntwo", New: "pair"},
	}})
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	if p.updated != "pair\nthree\n" || p.replaced != 2 {
		t.Fatalf("updated = %q replaced = %d", p.updated, p.replaced)
	}
}

func TestEditsBatchFailureNamesTheEdit(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.txt", "one\n")
	_, err := plan(t, wsp, editArgs{Path: "a.txt", Edits: []editOp{
		{Old: "one", New: "uno"},
		{Old: "missing", New: "x"},
	}})
	if err == nil || !strings.Contains(err.Error(), "edits[1]") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditsBatchRejectsMixingWithOldNew(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.txt", "one\n")
	_, err := prepareEdit(&core.ToolCall{Name: "edit", Arguments: []byte(`{"path":"a.txt","old":"one","new":"uno","edits":[{"old":"one","new":"x"}]}`)}, wsp)
	if err == nil || !strings.Contains(err.Error(), "unknown argument") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditsBatchRejectsEmptyOld(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.txt", "one\n")
	_, err := plan(t, wsp, editArgs{Path: "a.txt", Edits: []editOp{{Old: "", New: "x"}, {Old: "one", New: "two"}}})
	if err == nil || !strings.Contains(err.Error(), "edits[0]") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditsBatchMissingFileCreatesWhenUnambiguous(t *testing.T) {
	wsp, root := testPolicy(t)
	p, err := plan(t, wsp, editArgs{Path: "missing.txt", Edits: []editOp{{Old: "a", New: "b"}, {Old: "c", New: "b"}}})
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	if !p.created || p.updated != "b" || p.note == "" {
		t.Fatalf("plan = %+v", p)
	}
	if _, err := runPreparedEdit(t.Context(), nil, p); err != nil {
		t.Fatalf("runPreparedEdit: %v", err)
	}
	if content, _ := os.ReadFile(filepath.Join(root, "missing.txt")); string(content) != "b" {
		t.Fatalf("content = %q", content)
	}
}

func TestEditsBatchRejectsMissingFileWhenAmbiguous(t *testing.T) {
	wsp, _ := testPolicy(t)
	for _, edits := range [][]editOp{
		{{Old: "a", New: "b"}, {Old: "c", New: "d"}},
	} {
		_, err := plan(t, wsp, editArgs{Path: "missing.txt", Edits: edits})
		var toolErr *core.ToolError
		if !errors.As(err, &toolErr) || toolErr.Code != "missing_file" || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("edits %+v: err = %v", edits, err)
		}
	}
}

func TestDryRunReportsWithoutWriting(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.txt", "one\ntwo\n")
	p, err := plan(t, wsp, editArgs{Path: "a.txt", DryRun: true, Edits: []editOp{{Old: "two", New: "three"}}})
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	out, err := runPreparedEdit(t.Context(), nil, p)
	if err != nil {
		t.Fatalf("runPreparedEdit: %v", err)
	}
	if !strings.Contains(out, "\"dry_run\":true") || !strings.Contains(out, "\"replaced\":1") {
		t.Fatalf("result = %s", out)
	}
	content, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(content) != "one\ntwo\n" {
		t.Fatalf("dry run must not write, content = %q", content)
	}
}

func TestDeleteRemovesFile(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "scratch.py", "print(1)\n")
	p, err := plan(t, wsp, editArgs{Path: "scratch.py", Delete: true})
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	if !p.deleted {
		t.Fatalf("plan = %+v", p)
	}
	out, err := runPreparedEdit(t.Context(), nil, p)
	if err != nil {
		t.Fatalf("runPreparedEdit: %v", err)
	}
	if !strings.Contains(out, `"action":"delete"`) {
		t.Fatalf("result = %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "scratch.py")); !os.IsNotExist(err) {
		t.Fatalf("file still present, err = %v", err)
	}
}

func TestDeleteDryRunKeepsFile(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "keep.py", "x\n")
	p, err := plan(t, wsp, editArgs{Path: "keep.py", Delete: true, DryRun: true})
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	if _, err := runPreparedEdit(t.Context(), nil, p); err != nil {
		t.Fatalf("runPreparedEdit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "keep.py")); err != nil {
		t.Fatalf("dry run must not delete: %v", err)
	}
}

func TestDeleteRejectsCombinedWithEdit(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.txt", "one\n")
	if _, err := plan(t, wsp, editArgs{Path: "a.txt", Delete: true, Edits: []editOp{{Old: "one", New: "two"}}}); err == nil {
		t.Fatal("delete combined with an edit must be rejected")
	}
}

func TestDeleteRejectsMissingFileAndDirectory(t *testing.T) {
	wsp, root := testPolicy(t)
	if _, err := plan(t, wsp, editArgs{Path: "gone.txt", Delete: true}); err == nil {
		t.Fatal("deleting a missing file must be rejected")
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := plan(t, wsp, editArgs{Path: "sub", Delete: true}); err == nil {
		t.Fatal("deleting a directory must be rejected")
	}
}

func TestDeleteRejectsEscapingPath(t *testing.T) {
	wsp, _ := testPolicy(t)
	if _, err := plan(t, wsp, editArgs{Path: "../outside.txt", Delete: true}); err == nil {
		t.Fatal("delete must respect the workspace boundary")
	}
}

func TestDeleteAsksApproval(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.txt", "one\n")
	p, err := plan(t, wsp, editArgs{Path: "a.txt", Delete: true})
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	req := editApprovalRequest(p)
	if !strings.HasPrefix(req.Summary, "delete ") || req.Subject != "a.txt" {
		t.Fatalf("approval request = %+v", req)
	}
}

// The model asks for an empty file (a package `__init__.py`) as a non-empty
// old against a missing file, or as a batch of empty replacements; both
// create it empty (#453). A call carrying nothing at all still refuses --
// see TestPrepareEditRejectsEmptyCall.
func TestPrepareEditEmptyNewCreatesEmptyFile(t *testing.T) {
	for name, args := range map[string]editArgs{
		"oldset": {Path: "pkg/__init__.py", Edits: []editOp{{Old: "a"}}},
		"batch":  {Path: "pkg/__init__.py", Edits: []editOp{{Old: "a", New: ""}, {Old: "b", New: ""}}},
	} {
		wsp, root := testPolicy(t)
		p, err := plan(t, wsp, args)
		if err != nil {
			t.Fatalf("%s: prepareEdit: %v", name, err)
		}
		if !p.created || p.updated != "" {
			t.Fatalf("%s: plan = %+v", name, p)
		}
		if _, err := runPreparedEdit(t.Context(), nil, p); err != nil {
			t.Fatalf("%s: runPreparedEdit: %v", name, err)
		}
		if content, err := os.ReadFile(filepath.Join(root, "pkg/__init__.py")); err != nil || len(content) != 0 {
			t.Fatalf("%s: content = %q err = %v", name, content, err)
		}
	}
}

// An explicit empty replacement creates an empty file.
func TestPrepareEditExplicitEmptyFileUnderExistingDir(t *testing.T) {
	wsp, root := testPolicy(t)
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := plan(t, wsp, editArgs{Path: "pkg/__init__.py", Edits: []editOp{{Old: "", New: ""}}})
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	if !p.created || p.updated != "" || p.note != "" {
		t.Fatalf("plan = %+v", p)
	}
	if _, err := runPreparedEdit(t.Context(), nil, p); err != nil {
		t.Fatalf("runPreparedEdit: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "pkg/__init__.py")); err != nil || len(content) != 0 {
		t.Fatalf("content = %q err = %v", content, err)
	}
}

// Empty files can be created beneath missing parent directories.
func TestPrepareEditCreatesEmptyFileUnderMissingDir(t *testing.T) {
	wsp, root := testPolicy(t)
	p, err := plan(t, wsp, editArgs{Path: "pkg/sub/__init__.py", Edits: []editOp{{Old: "", New: ""}}})
	if err != nil || !p.created {
		t.Fatalf("plan = %+v err = %v", p, err)
	}
	if _, err := runPreparedEdit(t.Context(), nil, p); err != nil {
		t.Fatalf("runPreparedEdit: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "pkg/sub/__init__.py")); err != nil || len(content) != 0 {
		t.Fatalf("content = %q err = %v", content, err)
	}
}

func TestNormalizeArgKeysMapsAliasesAndRejectsUnknown(t *testing.T) {
	call := &core.ToolCall{Name: "edit", Arguments: []byte(`{"file":"a.py","edits":[{"old":"x","new":"y"}]}`)}
	if err := normalizeArgKeys(call); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	args, err := core.DecodeOptionalToolArgs[editArgs](call)
	if err != nil || args.Path != "a.py" || len(args.Edits) != 1 || args.Edits[0].Old != "x" || args.Edits[0].New != "y" {
		t.Fatalf("args = %+v err = %v", args, err)
	}
	call = &core.ToolCall{Name: "edit", Arguments: []byte(`{"path":"a.py","contents":"y"}`)}
	err = normalizeArgKeys(call)
	if err == nil || !strings.Contains(err.Error(), `unknown argument "contents"`) || !strings.Contains(err.Error(), "accepted: path, edits") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareEditRejectsContentAlias(t *testing.T) {
	wsp, _ := testPolicy(t)
	call := &core.ToolCall{Name: "edit", Arguments: []byte(`{"path":"new.py","content":"print(1)\n"}`)}
	p, err := prepareEdit(call, wsp)
	if err == nil || !strings.Contains(err.Error(), `unknown argument "content"`) {
		t.Fatalf("plan = %+v err = %v", p, err)
	}
}

func TestStripLinePrefixes(t *testing.T) {
	if got := stripLinePrefixes("line 3:     return 2\nline 4: }"); got != "    return 2\n}" {
		t.Fatalf("got %q", got)
	}
	if got := stripLinePrefixes("line 3: x\nplain"); got != "line 3: x\nplain" {
		t.Fatalf("mixed input must be left alone, got %q", got)
	}
}

func TestEditSuccessResultIsCompact(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.py", "a\nb\nc\nd\ne\nf\n")
	p, err := plan(t, wsp, editArgs{Path: "a.py", Edits: []editOp{{Old: "c\nd", New: "C"}}})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	out, err := runPreparedEdit(t.Context(), nil, p)
	if err != nil {
		t.Fatalf("runPreparedEdit: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("result = %q: %v", out, err)
	}
	if len(got) != 3 || got["path"] != "a.py" || got["action"] != "update" || got["replaced"] != float64(1) {
		t.Fatalf("unexpected success fields: %v", got)
	}
	if data, err := os.ReadFile(filepath.Join(root, "a.py")); err != nil || string(data) != "a\nb\nC\ne\nf\n" {
		t.Fatalf("written content = %q, err = %v", data, err)
	}
}

// TestBatchNotFoundHintQuotesTheFileOnDisk covers the rolled-back batch: a
// later edit that misses must be explained against the file as it is on disk,
// not against the buffer holding the earlier edits that were thrown away.
func TestBatchNotFoundHintQuotesTheFileOnDisk(t *testing.T) {
	args := editArgs{Edits: []editOp{
		{Old: "alpha", New: "ALPHA"},
		{Old: "gamna", New: "GAMMA"},
	}}
	_, _, err := applyOps("alpha\nbeta\ngamma\n", args, "f.go", "\n")
	if err == nil {
		t.Fatalf("want an error")
	}
	if strings.Contains(err.Error(), "ALPHA") {
		t.Fatalf("hint quotes the rolled-back buffer: %v", err)
	}
	if strings.Contains(err.Error(), "earlier edit invalidated") {
		t.Fatalf("unrelated missing anchor reported as a batch conflict: %v", err)
	}
	if !strings.Contains(err.Error(), "closest match in the file:\nalpha\nbeta\ngamma\n") {
		t.Fatalf("err = %v", err)
	}
}

// "old not found in file" dominated r10's tool failures (21 of 24). The
// tolerant rung matches text the model retyped with its own spacing — a tab
// where the file has spaces, doubled internal spaces, a trailing space — but
// only when it lands in one place.
func TestEditFileMatchesOldWithDifferentSpacing(t *testing.T) {
	for name, tc := range map[string]struct{ content, old string }{
		"tab for spaces":    {"func f() {\n    return 1\n}\n", "func f() {\n\treturn 1\n}"},
		"internal run":      {"import (\n\t\"os\"\n)\n\nfunc f(a, b int) {}\n", "func f(a,  b   int) {}"},
		"trailing space":    {"a := 1   \nb := 2\n", "a := 1\nb := 2"},
		"mixed indentation": {"if x {\n\t\ty = 2\n}\n", "if x {\n  y = 2\n}"},
	} {
		t.Run(name, func(t *testing.T) {
			got, count, err := replaceIn(tc.content, tc.old, "REPLACED", false, "\n")
			if err != nil || count != 1 {
				t.Fatalf("replaceIn: count = %d, err = %v", count, err)
			}
			if !strings.Contains(got, "REPLACED") {
				t.Fatalf("replacement not applied: %q", got)
			}
		})
	}
}

// An ambiguous tolerant match is refused rather than guessed at.
func TestEditFileRefusesAmbiguousSpacingMatch(t *testing.T) {
	content := "x  = 1\ny = 2\nx = 1\n"
	if _, _, err := replaceIn(content, "x   =  1", "z", false, "\n"); err == nil {
		t.Fatal("an ambiguous spacing-only match must not be applied")
	}
}

// The re-supplied window is the point of the refusal (#555): the model quotes
// the tool's own output on the retry instead of reconstructing the anchor.
func TestNotFoundResuppliesSurroundingLines(t *testing.T) {
	lines := make([]string, 0, 60)
	for i := range 60 {
		lines = append(lines, fmt.Sprintf("line %d", i+1))
	}
	content := strings.Join(lines, "\n") + "\n"
	err := notFoundError(content, "line 30\nline 31x")
	if err == nil {
		t.Fatalf("want an error")
	}
	got := err.Error()
	for _, want := range []string{"line 28\n", "line 30\n", "line 33\n", "current text, lines 28-33", "closest match to old starts at line 30", "do not retype it from memory"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "line 27\n") || strings.Contains(got, "line 34\n") {
		t.Fatalf("window is unbounded: %s", got)
	}
}

func TestNotFoundNoResemblanceQuotesSmallFileWhole(t *testing.T) {
	err := notFoundError("alpha\nbeta\ngamma\n", "zzzz")
	if err == nil || !strings.Contains(err.Error(), "nothing resembles") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "full current contents (3 lines):\nalpha\nbeta\ngamma") {
		t.Fatalf("err = %v", err)
	}
}

// A big file with nothing to point at is NOT dumped: flooding the context is
// the pressure that causes the fabrication in the first place.
func TestNotFoundNoResemblanceRefusesToDumpLargeFile(t *testing.T) {
	var b strings.Builder
	for i := range resupplyWholeFileLines + 1 {
		fmt.Fprintf(&b, "aaaa %d\n", i)
	}
	err := notFoundError(b.String(), "zzzz")
	if err == nil {
		t.Fatalf("want an error")
	}
	if strings.Contains(err.Error(), "full current contents") || !strings.Contains(err.Error(), "read it") {
		t.Fatalf("err = %v", err)
	}
	if len(err.Error()) > 400 {
		t.Fatalf("refusal is %d bytes, want a short one", len(err.Error()))
	}
}

// A successful edit is untouched by any of the above.
func TestSuccessfulEditUnaffectedByResupply(t *testing.T) {
	got, n, err := replaceIn("alpha\nbeta\ngamma\n", "beta", "BETA", false, "\n")
	if err != nil || n != 1 || got != "alpha\nBETA\ngamma\n" {
		t.Fatalf("got %q n=%d err=%v", got, n, err)
	}
}

func TestEditSchemaOnlyExposesEdits(t *testing.T) {
	schema := core.SchemaFor[editArgs]()
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"old", "new", "replace_all"} {
		if _, ok := decoded.Properties[key]; ok {
			t.Fatalf("legacy field %s exposed", key)
		}
	}
	if _, ok := decoded.Properties["edits"]; !ok {
		t.Fatal("edits missing")
	}
}

func TestEditRejectsMissingEditsAndLegacyFieldsWithoutWriting(t *testing.T) {
	for _, fields := range []string{``, `,"edits":[]`, `,"old":"one","new":"two"`, `,"replace_all":true`} {
		t.Run(fields, func(t *testing.T) {
			wsp, root := testPolicy(t)
			write(t, root, "a.txt", "one")
			for _, name := range []string{"a.txt", "missing.txt"} {
				args := fmt.Sprintf(`{"path":%q%s}`, name, fields)
				_, err := prepareEdit(&core.ToolCall{Name: "edit", Arguments: []byte(args)}, wsp)
				if err == nil {
					t.Fatalf("accepted %s", args)
				}
			}
			data, err := os.ReadFile(filepath.Join(root, "a.txt"))
			if err != nil || string(data) != "one" {
				t.Fatalf("file changed: %q %v", data, err)
			}
			if _, err := os.Stat(filepath.Join(root, "missing.txt")); !os.IsNotExist(err) {
				t.Fatalf("unexpected file: %v", err)
			}
		})
	}
}
