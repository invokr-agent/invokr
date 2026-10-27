package edit_file

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

func contentPlan(t *testing.T, wsp workspace.Policy, path, content string) (preparedEdit, error) {
	t.Helper()
	data, err := json.Marshal(map[string]string{"path": path, "content": content})
	if err != nil {
		t.Fatal(err)
	}
	return prepareEdit(&core.ToolCall{Name: "edit", Arguments: data}, wsp)
}

func replaceIn(content, oldText, newText string, all bool, eol string) (string, int, error) {
	spans, lineRung, err := replacementSpans(content, oldText, all)
	if err != nil {
		return "", 0, err
	}
	replacement, spans := replacementFor(content, newText, spans, lineRung, eol)
	for _, span := range slices.Backward(spans) {
		content = content[:span[0]] + replacement + content[span[1]:]
	}
	return content, len(spans), nil
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
	if err == nil || !strings.Contains(err.Error(), "nothing resembles") || !strings.Contains(err.Error(), "into oldText") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditRequiresExistingText(t *testing.T) {
	wsp, root := testPolicy(t)
	if _, err := plan(t, wsp, editArgs{Path: "new.txt", Edits: []editOp{{Old: "a", New: "b"}}}); err == nil || !strings.Contains(err.Error(), "use content") {
		t.Fatalf("missing file: %v", err)
	}
	write(t, root, "a.txt", "body\n")
	if _, err := plan(t, wsp, editArgs{Path: "a.txt", Edits: []editOp{{New: "fresh\n"}}}); err == nil || !strings.Contains(err.Error(), "use content") {
		t.Fatalf("empty oldText: %v", err)
	}
}

func TestPrepareEditRejectsEmptyOldAndNewOnExistingFile(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.txt", "body\n")
	if _, err := plan(t, wsp, editArgs{Path: "a.txt"}); err == nil {
		t.Fatal("expected error for empty old and new on existing file")
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

func TestEditsBatchMatchesOriginalFile(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.txt", "one\ntwo\nthree\n")
	p, err := plan(t, wsp, editArgs{Path: "a.txt", Edits: []editOp{
		{Old: "one", New: "uno"},
		{Old: "two", New: "dos"},
	}})
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	if p.updated != "uno\ndos\nthree\n" || p.replaced != 2 {
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

func TestEditsBatchRejectsMissingFile(t *testing.T) {
	wsp, _ := testPolicy(t)
	_, err := plan(t, wsp, editArgs{Path: "missing.txt", Edits: []editOp{{Old: "a", New: "b"}, {Old: "c", New: "d"}}})
	if err == nil || !strings.Contains(err.Error(), "use content") {
		t.Fatalf("err = %v", err)
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
	if p.operation != "delete" {
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

func TestEditContentCreatesAndOverwrites(t *testing.T) {
	wsp, root := testPolicy(t)
	for i, want := range []string{"", "fresh\n"} {
		p, err := contentPlan(t, wsp, "pkg/sub/__init__.py", want)
		if err != nil {
			t.Fatal(err)
		}
		action := []string{"create", "overwrite"}[i]
		if p.action("update") != action || editApprovalRequest(p).Summary != action+" pkg/sub/__init__.py" {
			t.Fatalf("action = %+v", p)
		}
		p.args.DryRun = true
		if _, err := runPreparedEdit(t.Context(), nil, p); err != nil {
			t.Fatal(err)
		}
		before, readErr := os.ReadFile(filepath.Join(root, "pkg/sub/__init__.py"))
		if (i == 0 && !os.IsNotExist(readErr)) || (i == 1 && (readErr != nil || len(before) != 0)) {
			t.Fatalf("dry run changed file: %q, %v", before, readErr)
		}
		if i == 0 {
			if _, err := os.Stat(filepath.Join(root, "pkg")); !os.IsNotExist(err) {
				t.Fatalf("dry run created parents: %v", err)
			}
		}
		p.args.DryRun = false
		if _, err := runPreparedEdit(t.Context(), nil, p); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(root, "pkg/sub/__init__.py"))
		if err != nil || string(got) != want {
			t.Fatalf("got %q, err %v", got, err)
		}
	}
}

func TestEditRequiresOperation(t *testing.T) {
	wsp, _ := testPolicy(t)
	_, err := prepareEdit(&core.ToolCall{Name: "edit", Arguments: []byte(`{"path":"empty.txt"}`)}, wsp)
	if err == nil || !strings.Contains(err.Error(), "edits must contain") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateArgKeysRejectsAliasesAndUnknown(t *testing.T) {
	call := &core.ToolCall{Name: "edit", Arguments: []byte(`{"file":"a.py","edits":[{"oldText":"x","newText":"y"}]}`)}
	if err := validateArgKeys(call); err == nil || !strings.Contains(err.Error(), `unknown argument "file"`) {
		t.Fatalf("alias accepted: %v", err)
	}
	call = &core.ToolCall{Name: "edit", Arguments: []byte(`{"path":"a.py","contents":"y"}`)}
	err := validateArgKeys(call)
	if err == nil || !strings.Contains(err.Error(), `unknown argument "contents"`) || !strings.Contains(err.Error(), "accepted: path, content, edits") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareEditAcceptsContent(t *testing.T) {
	wsp, _ := testPolicy(t)
	call := &core.ToolCall{Name: "edit", Arguments: []byte(`{"path":"new.py","content":"print(1)\n"}`)}
	p, err := prepareEdit(call, wsp)
	if err != nil || p.operation != "create" || p.updated != "print(1)\n" {
		t.Fatalf("plan = %+v err = %v", p, err)
	}
}

func TestPrepareEditRejectsContentWithEditsOrDelete(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.txt", "old\n")
	for _, raw := range []string{
		`{"path":"a.txt","content":"new\n","edits":[{"oldText":"old","newText":"new"}]}`,
		`{"path":"a.txt","content":"new\n","delete":true}`,
	} {
		if _, err := prepareEdit(&core.ToolCall{Name: "edit", Arguments: []byte(raw)}, wsp); err == nil || !strings.Contains(err.Error(), "content") {
			t.Fatalf("raw=%s err=%v", raw, err)
		}
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
	_, _, err := applyOriginalOps("alpha\nbeta\ngamma\n", args, "f.go", "\n")
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
	for _, want := range []string{"line 28\n", "line 30\n", "line 33\n", "current text, lines 28-33", "closest match to old starts at line 30", "do not retype it from memory", "into oldText"} {
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
	if err == nil || !strings.Contains(err.Error(), "nothing resembles") || !strings.Contains(err.Error(), "into oldText") {
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
	if strings.Contains(err.Error(), "full current contents") || !strings.Contains(err.Error(), "read it") || !strings.Contains(err.Error(), "into oldText") {
		t.Fatalf("err = %v", err)
	}
	if len(err.Error()) > 400 {
		t.Fatalf("refusal is %d bytes, want a short one", len(err.Error()))
	}
}

func TestSuccessfulEditUnaffectedByResupply(t *testing.T) {
	got, n, err := replaceIn("alpha\nbeta\ngamma\n", "beta", "BETA", false, "\n")
	if err != nil || n != 1 || got != "alpha\nBETA\ngamma\n" {
		t.Fatalf("got %q n=%d err=%v", got, n, err)
	}
}

func TestEditSchemaOnlyExposesEdits(t *testing.T) {
	schema := editSchema()
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
	for _, key := range []string{"oldText", "newText", "replace_all"} {
		if _, ok := decoded.Properties[key]; ok {
			t.Fatalf("legacy field %s exposed", key)
		}
	}
	if _, ok := decoded.Properties["edits"]; !ok {
		t.Fatal("edits missing")
	}
	oldAt, newAt := strings.Index(string(data), `"oldText"`), strings.Index(string(data), `"newText"`)
	if oldAt < 0 || newAt < 0 || oldAt >= newAt {
		t.Fatalf("provider schema must put oldText before newText: %s", data)
	}
}

func TestEditRejectsLegacyPair(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.txt", "one")
	_, err := prepareEdit(&core.ToolCall{Name: "edit", Arguments: []byte(`{"path":"a.txt","edits":[{"old":"one","new":"two"}]}`)}, wsp)
	if err == nil {
		t.Fatalf("legacy pair accepted: %v", err)
	}
}

func TestEditRequiresNewTextButAllowsEmptyReplacement(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.txt", "one")
	_, err := prepareEdit(&core.ToolCall{Name: "edit", Arguments: []byte(`{"path":"a.txt","edits":[{"oldText":"one"}]}`)}, wsp)
	if err == nil || !strings.Contains(err.Error(), "requires newText") {
		t.Fatalf("missing newText: %v", err)
	}
	if _, err := prepareEdit(&core.ToolCall{Name: "edit", Arguments: []byte(`{"path":"a.txt","edits":[{"oldText":"one","newText":""}]}`)}, wsp); err != nil {
		t.Fatalf("empty replacement: %v", err)
	}
}

func TestOverlappingBatchLeavesFileUnchanged(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "a.txt", "alpha beta\n")
	_, err := plan(t, wsp, editArgs{Path: "a.txt", Edits: []editOp{{Old: "alpha beta", New: "x"}, {Old: "beta", New: "y"}}})
	if err == nil || !strings.Contains(err.Error(), "overlapping edits") {
		t.Fatalf("overlap: %v", err)
	}
	got, readErr := os.ReadFile(filepath.Join(root, "a.txt"))
	if readErr != nil || string(got) != "alpha beta\n" {
		t.Fatalf("file changed: %q, %v", got, readErr)
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
