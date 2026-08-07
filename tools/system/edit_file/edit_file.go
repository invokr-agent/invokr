// Package edit_file implements a search/replace file edit tool: a low-ceremony
// alternative to the apply_patch envelope. The edit structure lives in typed
// schema fields (path/old/new) rather than in a freeform patch string, which
// weaker models handle far more reliably than diff syntax.
package edit_file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/support/fsutil"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

type editArgs struct {
	Path   string   `json:"path" desc:"Workspace-relative or absolute path to edit."`
	Edits  []editOp `json:"edits,omitempty" desc:"One or more replacements; required unless delete is true. Batch changes to this file in one call. Applied in order against updated content; all apply or none do. A single empty old creates or overwrites the whole file."`
	DryRun bool     `json:"dry_run,omitempty" desc:"Validate the edit and report the outcome without writing."`
	Delete bool     `json:"delete,omitempty" desc:"Delete the file at path instead of editing it. Give no edits with it."`
}

type editOp struct {
	Old        string `json:"old" desc:"Exact text to find. Empty only in a single-entry list to create or overwrite the whole file."`
	New        string `json:"new" desc:"Replacement text. Empty deletes matched text."`
	ReplaceAll bool   `json:"replace_all,omitempty" desc:"Replace every match instead of requiring a unique one."`
}

type preparedEdit struct {
	args                                    editArgs
	original, displayPath, absPath, updated string
	mode                                    os.FileMode
	created, overwrote, deleted             bool
	replaced                                int
	// note tells the model when the call was repaired rather than applied as
	// sent, so it learns what actually happened to the file.
	note string
}

type editResponse struct {
	Path     string `json:"path"`
	Action   string `json:"action"`
	Replaced int    `json:"replaced"`
	DryRun   bool   `json:"dry_run,omitempty"`
	Note     string `json:"note,omitempty"`
}

func RegisterEditFileTool(c core.NativeRegistrar, dispatcher events.Dispatcher, wsp workspace.Policy) {
	core.Register(c, core.Tool[preparedEdit]{
		Spec: core.ToolSpec{
			Name:        "edit",
			Description: "Edit a workspace file by exact search and replace. Pass an `edits` list even for one replacement, with exact existing text in `old` and its replacement in `new`; `old` must match exactly one place unless replace_all is set. Use a single edit with empty `old` to create or overwrite the whole file, including an empty file. For several changes to one file, use one call with multiple `edits` instead of separate calls. Keep each `old` small but unique; merge overlapping changes. Edits match the content after earlier edits in the list. Set dry_run to validate without writing. To remove a file, pass `delete` with the path and nothing else — this is the only way to delete a file.",
			Example:     `{"name":"edit","arguments":{"path":"README.md","edits":[{"old":"## Usage","new":"## Usage\n\nRun the CLI."}]}}`,
			Parameters:  core.SchemaFor[editArgs](),
			Metadata:    core.WriteWorkspaceAskMetadata("workspace", core.ToolCapabilities{ReadsWorkspace: true, WritesWorkspace: true}),
		},
		Decode: func(call *core.ToolCall) (preparedEdit, error) {
			return prepareEdit(call, wsp)
		},
		Approve: func(ctx context.Context, plan preparedEdit) core.ApprovalDecision {
			req := editApprovalRequest(plan)
			req.Confined, req.Writes, req.ReadOnly = true, !plan.args.DryRun, plan.args.DryRun
			req.Paths, req.Protected = []string{plan.displayPath}, !plan.args.DryRun && wsp.IsProtected(plan.absPath)
			// A dry run writes nothing, so it never needs a prompt.
			if plan.args.DryRun {
				return core.Automatic(req)
			}
			return core.Ask(req)
		},
		Approver: core.ApproverFrom(c),
		AuditFailure: func(ctx context.Context, plan preparedEdit, err error) {
			dispatchEditAudit(ctx, dispatcher, plan, "rejected", err)
		},
		Execute: func(ctx context.Context, plan preparedEdit) (string, error) {
			return runPreparedEdit(ctx, dispatcher, plan)
		},
	})
}

func prepareEdit(call *core.ToolCall, wsp workspace.Policy) (preparedEdit, error) {
	if err := normalizeArgKeys(call); err != nil {
		return preparedEdit{}, err
	}
	args, err := core.DecodeOptionalToolArgs[editArgs](call)
	if err != nil {
		return preparedEdit{}, fmt.Errorf("parse edit arguments: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return preparedEdit{}, fmt.Errorf("path is required")
	}
	abs, err := wsp.ResolveWrite(args.Path)
	if err != nil {
		return preparedEdit{}, err
	}
	plan := preparedEdit{args: args, displayPath: wsp.RelDisplayPath(abs, args.Path), absPath: abs, mode: 0o644}

	if args.Delete {
		return prepareDelete(plan)
	}
	if len(args.Edits) == 0 {
		return preparedEdit{}, fmt.Errorf("edit: edits must contain at least one replacement; use a single empty old to create or overwrite a file")
	}
	content, err := os.ReadFile(abs)
	switch {
	case os.IsNotExist(err):
		return prepareCreate(plan)
	case err != nil:
		return preparedEdit{}, fmt.Errorf("read %s: %w", plan.displayPath, err)
	}
	if info, statErr := os.Stat(abs); statErr == nil {
		plan.mode = info.Mode().Perm()
	}
	plan.original = string(content)
	return prepareExistingEdit(plan, string(content))
}

// prepareExistingEdit plans a change to a file that already exists.
func prepareExistingEdit(plan preparedEdit, content string) (preparedEdit, error) {
	args := plan.args
	// An empty old with replacement text on a file that exists is the model
	// asking to rewrite it whole. Rejecting that produced retry loops (the
	// model resends the same call), and its intent is unambiguous, so honor it.
	if len(args.Edits) == 1 && args.Edits[0].Old == "" {
		plan.overwrote, plan.replaced, plan.updated = true, 1, args.Edits[0].New
		return plan, nil
	}
	updated, replaced, err := applyOps(content, args, plan.displayPath, workspace.DominantEOL(content))
	if err != nil {
		return preparedEdit{}, err
	}
	plan.updated, plan.replaced = updated, replaced
	return plan, nil
}

// runDelete removes the file, or reports what it would remove on a dry run.
func runDelete(ctx context.Context, dispatcher events.Dispatcher, plan preparedEdit) (string, error) {
	if !plan.args.DryRun {
		if err := os.Remove(plan.absPath); err != nil {
			dispatchEditAudit(ctx, dispatcher, plan, "failed", err)
			return "", fmt.Errorf("delete %s: %w", plan.displayPath, err)
		}
	}
	resp := editResponse{Path: plan.displayPath, Action: "delete", DryRun: plan.args.DryRun}
	return finishEdit(ctx, dispatcher, plan, "completed", resp)
}

// finishEdit encodes the result and records the audit outcome of a call that
// did what it was asked to.
func finishEdit(ctx context.Context, dispatcher events.Dispatcher, plan preparedEdit, outcome string, resp editResponse) (string, error) {
	data, err := core.MarshalResult("edit", resp)
	if err != nil {
		return "", err
	}
	dispatchEditAudit(ctx, dispatcher, plan, outcome, nil)
	return data, nil
}

// prepareDelete validates a delete request: the path must name an existing
// regular file inside the workspace (wsp.Resolve already enforced that), and
// no edit fields may accompany it, so "delete" can never be a silent
// alternative outcome of a call that also asked for an edit.
func prepareDelete(plan preparedEdit) (preparedEdit, error) {
	args := plan.args
	if len(args.Edits) > 0 {
		return preparedEdit{}, fmt.Errorf("delete %s: give delete on its own, without edits", plan.displayPath)
	}
	info, err := os.Stat(plan.absPath)
	if os.IsNotExist(err) {
		return preparedEdit{}, fmt.Errorf("cannot delete %s: file does not exist", plan.displayPath)
	}
	if err != nil {
		return preparedEdit{}, fmt.Errorf("stat %s: %w", plan.displayPath, err)
	}
	if info.IsDir() {
		return preparedEdit{}, fmt.Errorf("cannot delete %s: it is a directory; only files can be deleted", plan.displayPath)
	}
	plan.deleted, plan.mode = true, info.Mode().Perm()
	return plan, nil
}

// prepareCreate plans the creation of a missing file. The documented shape is
// an empty old with the contents in new, but the most common failed call is a
// non-empty old against a file that is not there yet: the model wanted the
// file to end up containing new, so create it from new and say so in the
// result instead of bouncing the call. An empty new creates an empty file
// (the model's `__init__.py`); only an edits batch with conflicting
// replacements is still refused.
func prepareCreate(plan preparedEdit) (preparedEdit, error) {
	args := plan.args
	content, err := createContent(args, plan.displayPath)
	if err != nil {
		return preparedEdit{}, err
	}
	plan.created, plan.replaced, plan.updated = true, 1, content
	if len(args.Edits) > 1 || args.Edits[0].Old != "" {
		plan.note = fmt.Sprintf("%s did not exist, so it was created with new as its whole content; old was ignored because there was no existing text to match", plan.displayPath)
	}
	return plan, nil
}

// createContent picks the full contents for a missing file from the call's
// shape. An empty new is a legitimate request for an empty file — the
// package `__init__.py` in a directory that does not exist yet was the single
// largest edit failure on the bench (39 per arm) while the parent
// directory gated it; MkdirAll on write covers the nesting. Only an edits
// batch that disagrees with itself is refused.
func createContent(args editArgs, display string) (string, error) {
	content := args.Edits[0].New
	for _, op := range args.Edits[1:] {
		if op.New != content {
			return "", &core.ToolError{Code: "missing_file", Err: fmt.Errorf("cannot edit %s: file does not exist and the edits disagree on its contents; create it first with an empty old and the full contents in new", display)}
		}
	}
	return content, nil
}

// applyOps applies the edits in order against the evolving content — and returns the final content
// with the total replacement count. A batch is all-or-nothing: the first
// failing edit aborts the whole call.
func applyOps(content string, args editArgs, display, eol string) (string, int, error) {
	ops, err := editOpsFor(args, display)
	if err != nil {
		return "", 0, err
	}
	total := 0
	original := content
	for i, op := range ops {
		updated, replaced, err := replaceIn(content, op.Old, op.New, op.ReplaceAll, eol)
		if err != nil {
			if errors.Is(err, errOldNotFound) {
				err = notFoundError(original, op.Old)
				if _, _, originalErr := replaceIn(original, op.Old, op.New, op.ReplaceAll, eol); i > 0 && originalErr == nil {
					err = errors.New("old matched the original file, but an earlier edit invalidated it. Edits apply sequentially. No changes were written. Merge overlapping edits or make this old match the result of earlier edits")
				}
			}
			return "", 0, fmt.Errorf("edit %s: edits[%d]: %w", display, i, err)
		}
		content, total = updated, total+replaced
	}
	return content, total, nil
}

// editOpsFor validates replacements on an existing file.
func editOpsFor(args editArgs, display string) ([]editOp, error) {
	for i, op := range args.Edits {
		if op.Old == "" {
			return nil, fmt.Errorf("edit %s: edits[%d]: empty old requires a single-entry edits list for whole-file overwrite", display, i)
		}
		args.Edits[i].Old = stripLinePrefixes(op.Old)
	}
	return args.Edits, nil
}

// toEOL rewrites every line ending in s to eol.
func toEOL(s, eol string) string {
	lf := strings.ReplaceAll(s, "\r\n", "\n")
	if eol == "\n" {
		return lf
	}
	return strings.ReplaceAll(lf, "\n", eol)
}

// stripCRLine ignores a line's carriage return, so LF `old` matches a CRLF
// file (and the reverse) without also tolerating trailing-whitespace drift.
func stripCRLine(s string) string { return strings.TrimSuffix(s, "\r") }

// replaceIn applies the matching ladder: exact text, then line matching that
// ignores line endings, then line matching that ignores trailing whitespace,
// then line matching that ignores all leading and trailing whitespace. The
// first rung with any match wins. Matching is what the rungs differ on; the
// replacement always carries the file's own line ending, whichever rung hit.
func replaceIn(content, oldText, newText string, all bool, eol string) (string, int, error) {
	rungs := []func(string) string{nil, stripCRLine, trimRightLine, strings.TrimSpace, collapseSpacesLine}
	for i, norm := range rungs {
		spans := locate(content, oldText, norm)
		if len(spans) == 0 {
			continue
		}
		// The loosest rung ignores the model's spacing altogether, so it is
		// only trusted when it lands in exactly one place: two regions equal
		// modulo spacing are a coin flip, and an edit written to the wrong one
		// is worse than the "old not found" the model can retry from.
		if i == len(rungs)-1 && len(spans) > 1 {
			break
		}
		if len(spans) > 1 && !all {
			return "", 0, fmt.Errorf("old matches %d locations (first at offsets %d and %d); include surrounding lines to make it unique, or set replace_all", len(spans), spans[0][0], spans[1][0])
		}
		if !all {
			spans = spans[:1]
		}
		replacement, spans := replacementFor(content, newText, spans, norm != nil, eol)
		return spliceAll(content, spans, replacement), len(spans), nil
	}
	if spans := locateBlockAnchor(content, oldText); len(spans) == 1 && !all {
		replacement, spans := replacementFor(content, newText, spans, true, eol)
		return spliceAll(content, spans, replacement), 1, nil
	}
	return "", 0, errOldNotFound
}

// Block-anchor budgets (#577). The five ladder rungs all require every line of
// old to match once whitespace is normalized, so a block whose interior the
// model paraphrased fails whole — 24% of the failed multi-line edits in the
// pod M1 runs had a region whose first and last lines still matched exactly.
// A candidate must sit within a quarter of old's line count (opencode's band,
// edit.ts:303) and score on a character Levenshtein similarity over the
// trimmed interior. The threshold is 0.85, not opencode's shipped 0.65
// (edit.ts:220) — a deliberate deviation, not a transcription slip: opencode
// pairs 0.65 with best-candidate scoring and an LSP pass that surfaces a
// broken write in the same tool result, and invokr finds a bad write at the
// next build or not at all. In the pod M1 runs 21 of the 24 rescuable edits
// scored above 0.85, so the looser value buys coverage too small to measure
// at n=6 while widening the window for a wrong-region write, which is the
// asymmetric harm and the one n=6 cannot detect. blockAnchorMaxGrowth is ours: a
// candidate that matches at both anchors but carries several times old's text
// is a different region that happens to start and end alike.
const (
	blockAnchorMinLines      = 3
	blockAnchorSizeTolerance = 0.25
	blockAnchorMinSimilarity = 0.85
	blockAnchorMaxGrowth     = 2
	blockAnchorMaxCells      = 4 << 20
)

// locateBlockAnchor matches a multi-line old by its first line, trimmed,
// accepting the candidate block whose remaining lines score closest. The last
// line is not an anchor: replaying the #577 M1 failures, a first-line-only
// anchor rescued 9 of 23 at 0.85 against 7 for first+last. It is the
// ladder's last resort, so it returns every candidate and leaves the caller to
// insist on exactly one: an edit written to the wrong region is worse than the
// "old not found" the model can retry from. opencode takes its best-scoring
// candidate instead; requiring uniqueness costs coverage and buys the
// guarantee that a near-tie is never resolved silently.
func locateBlockAnchor(content, old string) [][2]int {
	want := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	if len(want) < blockAnchorMinLines {
		return nil
	}
	lines, offsets := splitWithOffsets(content)
	first := strings.TrimSpace(want[0])
	delta := max(1, int(float64(len(want))*blockAnchorSizeTolerance))
	sizes := []int{len(want)}
	for d := 1; d <= delta; d++ {
		if len(want)-d >= blockAnchorMinLines {
			sizes = append(sizes, len(want)-d)
		}
		sizes = append(sizes, len(want)+d)
	}
	var spans [][2]int
	for i := range lines {
		if strings.TrimSpace(lines[i]) != first {
			continue
		}
		if span, ok := bestBlockAt(lines, offsets, i, want, sizes, len(old)); ok {
			spans = append(spans, span)
		}
	}
	return spans
}

// bestBlockAt scores the blocks starting at line i, one per candidate size, and
// returns the best above the similarity floor. There is no last-line anchor,
// so the end is wherever the rest of old scores best; sizes arrive nearest
// old's own line count first, so a tie keeps the block old describes instead
// of swallowing a trailing blank line.
func bestBlockAt(lines []string, offsets []int, i int, want []string, sizes []int, oldLen int) ([2]int, bool) {
	best, bestScore := [2]int{}, 0.0
	for _, size := range sizes {
		end := i + size - 1
		if end >= len(lines) {
			continue
		}
		span := [2]int{offsets[i], offsets[end] + len(lines[end])}
		if span[1]-span[0] > blockAnchorMaxGrowth*oldLen {
			continue
		}
		if score := lineSimilarity(want[1:], lines[i+1:end+1]); score >= blockAnchorMinSimilarity && score > bestScore {
			best, bestScore = span, score
		}
	}
	return best, bestScore > 0
}

// lineSimilarity scores two blocks by Levenshtein distance over their trimmed
// UTF-8 bytes, as 1 - distance/byte length of the longer. Iterate every byte:
// ranging over strings skips continuation-byte cells and can give different
// Unicode interiors a perfect score. Keep the existing byte-based work bound
// and ASCII-calibrated threshold, rather than changing the matching policy.
func lineSimilarity(want, got []string) float64 {
	a, b := trimJoin(want), trimJoin(got)
	if a == b {
		return 1
	}
	if a == "" || b == "" || len(a)*len(b) > blockAnchorMaxCells {
		return 0 // a block that big is not what this rung is for, and the score is quadratic
	}
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := range len(a) {
		cur := make([]int, len(b)+1)
		cur[0] = i + 1
		for j := range len(b) {
			cost := 1
			if a[i] == b[j] {
				cost = 0
			}
			cur[j+1] = min(prev[j]+cost, min(cur[j]+1, prev[j+1]+1))
		}
		prev = cur
	}
	return 1 - float64(prev[len(b)])/float64(max(len(a), len(b)))
}

func trimJoin(lines []string) string {
	return strings.Join(core.MapSlice(lines, strings.TrimSpace), "\n")
}

// replacementFor shapes the replacement text for the rung that matched, and
// pulls the spans back off a CRLF terminator where needed. Line-based rungs
// match whole lines without their terminator, so the replacement must shed its
// own trailing newline too. Either way it carries the file's own line ending
// rather than whatever the model sent: the model that quotes "old" faithfully
// out of read_file is exactly the one that then hand-writes "new" with bare
// newlines.
func replacementFor(content, newText string, spans [][2]int, lineRung bool, eol string) (string, [][2]int) {
	if !lineRung {
		return toEOL(newText, eol), spans
	}
	if eol == "\r\n" {
		spans = keepTrailingCR(content, spans)
	}
	return toEOL(strings.TrimSuffix(toEOL(newText, "\n"), "\n"), eol), spans
}

// errOldNotFound is a sentinel so the caller can build the "closest match"
// hint against the file on disk rather than the partly-edited buffer: a
// failed batch is rolled back, so a hint quoting the buffer would tell the
// model to copy text that is not in the file.
var errOldNotFound = errors.New("old not found in file")

// keepTrailingCR pulls a span back off the carriage return that terminates its
// last line, so a CRLF file keeps its own \r\n around the spliced replacement.
func keepTrailingCR(content string, spans [][2]int) [][2]int {
	for i, span := range spans {
		if span[1] > span[0] && content[span[1]-1] == '\r' {
			spans[i][1] = span[1] - 1
		}
	}
	return spans
}

func spliceAll(content string, spans [][2]int, replacement string) string {
	var b strings.Builder
	cursor := 0
	for _, span := range spans {
		b.WriteString(content[cursor:span[0]])
		b.WriteString(replacement)
		cursor = span[1]
	}
	b.WriteString(content[cursor:])
	return b.String()
}

// locate returns the byte spans of old in content. A nil norm means exact
// substring search; otherwise lines are compared after norm.
func locate(content, old string, norm func(string) string) [][2]int {
	if norm == nil {
		return locateExact(content, old)
	}
	return locateLines(content, old, norm)
}

func locateExact(content, old string) [][2]int {
	var spans [][2]int
	for from := 0; from <= len(content); {
		i := strings.Index(content[from:], old)
		if i < 0 {
			break
		}
		start := from + i
		spans = append(spans, [2]int{start, start + len(old)})
		from = start + max(len(old), 1)
	}
	return spans
}

func locateLines(content, old string, norm func(string) string) [][2]int {
	lines, offsets := splitWithOffsets(content)
	want := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	var spans [][2]int
	for i := 0; i+len(want) <= len(lines); i++ {
		if !slices.EqualFunc(lines[i:i+len(want)], want, func(a, b string) bool { return norm(a) == norm(b) }) {
			continue
		}
		last := i + len(want) - 1
		spans = append(spans, [2]int{offsets[i], offsets[last] + len(lines[last])})
		i = last
	}
	return spans
}

func splitWithOffsets(content string) ([]string, []int) {
	lines := strings.Split(content, "\n")
	offsets := make([]int, len(lines))
	at := 0
	for i, line := range lines {
		offsets[i] = at
		at += len(line) + 1
	}
	return lines, offsets
}

func trimRightLine(s string) string { return strings.TrimRight(s, " \t\r") }

// Bound the context around a suggested match and the whole-file fallback.
const (
	resupplyContext        = 2
	resupplyWholeFileLines = 200
	resupplyWholeFileBytes = 8000
)

// collapseSpacesLine trims a line and squeezes its internal blank runs to one
// space, so text the model retyped with its own spacing ("func f(a, b  int)",
// a tab where the file has spaces) still finds the line it means. "old not
// found in file" was 21 of the round's 24 tool failures (r10 H3), most of them
// on import blocks and comment headers reconstructed from memory.
func collapseSpacesLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// notFoundError re-supplies the file's actual text at the point of refusal,
// so the retry can quote the tool's own output instead of the model's memory.
// Candidate ranking is diagnostic only; it never authorizes a replacement.
func notFoundError(content, old string) error {
	want := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	lines, _ := splitWithOffsets(content)
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1] // the terminator's phantom trailing line
	}
	bestIdx, bestScore := -1, 0
	for i := range lines {
		if score := windowScore(lines, want, i); score > bestScore {
			bestIdx, bestScore = i, score
		}
	}
	if bestIdx < 0 {
		return noResemblanceError(content, lines, want[0])
	}
	from, to := max(0, bestIdx-resupplyContext), min(bestIdx+len(want)+resupplyContext, len(lines))
	var b strings.Builder
	fmt.Fprintf(&b, "old not found in file; suggested closest match to old starts at line %d.\n", bestIdx+1)
	writeFirstDifference(&b, lines, want, bestIdx)
	fmt.Fprintf(&b, "Copy the raw text below verbatim into old — do not retype it from memory. File's current text, lines %d-%d; closest match in the file:\n", from+1, to)
	for i := from; i < to; i++ {
		b.WriteString(lines[i])
		b.WriteString("\n")
	}
	return fmt.Errorf("%s", b.String())
}

// writeFirstDifference names the first line of old that the file disagrees
// with, which is usually enough on its own for a near-miss retry.
func writeFirstDifference(b *strings.Builder, lines, want []string, bestIdx int) {
	for j, line := range want {
		if bestIdx+j < len(lines) && strings.TrimSpace(lines[bestIdx+j]) == strings.TrimSpace(line) {
			continue
		}
		got := ""
		if bestIdx+j < len(lines) {
			got = lines[bestIdx+j]
		}
		fmt.Fprintf(b, "first difference at old line %d: you gave %q, the file has %q\n", j+1, line, got)
		return
	}
}

// noResemblanceError handles the miss with no close match at all — the model
// invented the anchor wholesale, so there is no window to point at. A file
// small enough to cost less than the re-read it would otherwise provoke is
// returned whole; a larger one is not, because flooding the context is the
// very pressure that caused the fabrication. The model re-reads that one, as
// it does today.
func noResemblanceError(content string, lines []string, first string) error {
	if len(lines) <= resupplyWholeFileLines && len(content) <= resupplyWholeFileBytes {
		return fmt.Errorf("old not found in file; nothing resembles %q. Copy the raw text below verbatim into old — do not retype it from memory. The file's full current contents (%d lines):\n%s", first, len(lines), strings.TrimSuffix(content, "\n"))
	}
	return fmt.Errorf("old not found in file; nothing resembles %q. The file is %d lines, too large to quote here: read it and copy the exact text into old — do not retype it from memory", first, len(lines))
}

// Exact lines dominate; bounded whole-line similarity breaks ties without
// favoring an unrelated line that happens to share a longer prefix. Similarity
// must exceed 50% to suggest a candidate without any exact matching lines.
func windowScore(lines, want []string, start int) int {
	score := 0
	for j, line := range want {
		if start+j >= len(lines) {
			break
		}
		got, exp := strings.TrimSpace(lines[start+j]), strings.TrimSpace(line)
		switch {
		case got == exp && exp != "":
			score += 1000
		case j == 0 && exp != "":
			score += max(0, int(100*lineSimilarity([]string{exp}, []string{got}))-50)
		}
	}
	return score
}

func editApprovalRequest(plan preparedEdit) approval.Request {
	return approval.Request{Tool: "edit", Reason: "workspace write", Tier: "write_workspace", Summary: fmt.Sprintf("%s %s", plan.action("edit"), plan.displayPath), Details: fmt.Sprintf("Replacements: %d\n\n%s", plan.replaced, describeOps(plan.args)), Subject: plan.displayPath, SubjectIsPathList: true}
}

// describeOps renders the requested change for the approval prompt, whichever
// shape the call used.
func describeOps(args editArgs) string {
	if args.Delete {
		return "Delete the file."
	}
	var b strings.Builder
	for i, op := range args.Edits {
		fmt.Fprintf(&b, "Edit %d old:\n%s\n\nEdit %d new:\n%s\n\n", i+1, op.Old, i+1, op.New)
	}
	return strings.TrimSuffix(b.String(), "\n\n")
}

func runPreparedEdit(ctx context.Context, dispatcher events.Dispatcher, plan preparedEdit) (string, error) {
	if plan.deleted {
		return runDelete(ctx, dispatcher, plan)
	}
	if !plan.args.DryRun {
		if err := os.MkdirAll(filepath.Dir(plan.absPath), 0o755); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", plan.displayPath, err)
		}
		if err := fsutil.WriteFileAtomic(plan.absPath, []byte(plan.updated), plan.mode); err != nil {
			dispatchEditAudit(ctx, dispatcher, plan, "failed", err)
			return "", fmt.Errorf("write %s: %w", plan.displayPath, err)
		}
	}
	return finishEdit(ctx, dispatcher, plan, "applied", editResponse{Path: plan.displayPath, Action: plan.action("update"), Replaced: plan.replaced, DryRun: plan.args.DryRun, Note: plan.note})
}

// action names what the plan does to the file; fallback is the plain-edit
// word, which the approval prompt and the result spell differently.
func (p preparedEdit) action(fallback string) string {
	switch {
	case p.created:
		return "create"
	case p.overwrote:
		return "overwrite"
	case p.deleted:
		return "delete"
	}
	return fallback
}

func dispatchEditAudit(ctx context.Context, dispatcher events.Dispatcher, plan preparedEdit, outcome string, err error) {
	core.DispatchAudit(ctx, dispatcher, &events.AuditPayload{Action: "edit", Tool: "edit", Paths: []string{plan.displayPath}, DryRun: plan.args.DryRun, Outcome: outcome}, err)
}

// argAliases accepts common path spellings without introducing another edit shape.
var argAliases = map[string]string{"file": "path", "file_path": "path", "filename": "path"}

var knownArgKeys = []string{"path", "edits", "dry_run", "delete"}

// normalizeArgKeys rewrites aliased keys in place and refuses unknown ones
// with the list of accepted names, instead of decoding around them.
func normalizeArgKeys(call *core.ToolCall) error {
	if call == nil || len(call.Arguments) == 0 {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(call.Arguments, &raw); err != nil {
		return nil // let the typed decode report the syntax error
	}
	changed := false
	for key, value := range raw {
		canonical, aliased := argAliases[key]
		switch {
		case aliased:
			if _, taken := raw[canonical]; !taken {
				raw[canonical] = value
			}
			delete(raw, key)
			changed = true
		case !slices.Contains(knownArgKeys, key):
			return fmt.Errorf("edit: unknown argument %q; accepted: %s", key, strings.Join(knownArgKeys, ", "))
		}
	}
	if changed {
		call.Arguments, _ = json.Marshal(raw)
	}
	return nil
}

var linePrefix = regexp.MustCompile(`^line \d+: `)

// stripLinePrefixes removes a "line N: " prefix from every line of old when
// all of them carry one — the model copied a previous error hint back.
func stripLinePrefixes(old string) string {
	lines := strings.Split(old, "\n")
	if slices.ContainsFunc(lines, func(line string) bool {
		return strings.TrimSpace(line) != "" && !linePrefix.MatchString(line)
	}) {
		return old
	}
	return strings.Join(core.MapSlice(lines, func(line string) string {
		return linePrefix.ReplaceAllString(line, "")
	}), "\n")
}
