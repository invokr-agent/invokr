package apply_patch

import (
	"context"
	"fmt"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/workspace"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/support/fsutil"
)

type applyPatchArgs struct {
	Patch  string `json:"patch" desc:"Patch in Invokr apply_patch format."`
	DryRun bool   `json:"dry_run,omitempty" desc:"Validate and preview patch without mutating files."`
}

type applyPatchResponse struct {
	ChangedFiles []applyPatchChangedFile `json:"changed_files"`
	Hunks        int                     `json:"hunks"`
	DryRun       bool                    `json:"dry_run"`
}

type preparedApplyPatch struct {
	args      applyPatchArgs
	patchText string
	ops       []applyPatchOp
	hunkCount int
	changes   []plannedFileChange
}

type applyPatchChangedFile struct {
	Path           string `json:"path"`
	Action         string `json:"action"`
	PreviousSHA256 string `json:"previous_sha256,omitempty"`
	NewSHA256      string `json:"new_sha256,omitempty"`
}

type applyPatchOp struct {
	Action, Path string
	Hunks        []patchHunk
}

type patchHunk struct {
	Lines []patchLine
}

type patchLine struct {
	Kind byte
	Text string
}

type plannedFileChange struct {
	displayPath, absPath, action, original, originalSHA256, updated string
	existed                                                         bool
	mode                                                            os.FileMode
}

type rollbackState struct {
	change           plannedFileChange
	backup           string
	created, applied bool
}

// ponytail: one process-wide lock safely serializes overlapping patches; use keyed locks only if patch throughput matters.
var patchCommitMu sync.Mutex

// RegisterApplyPatchTool registers the patch tool. With advertise false (the
// default posture) it is deferred: absent from the advertised tool list, found
// via tool_search when a model looks for patch/diff/multi-file editing.
func RegisterApplyPatchTool(c core.NativeRegistrar, dispatcher events.Dispatcher, workspace workspace.Policy, advertise bool) {
	metadata := core.WriteWorkspaceAskMetadata("workspace", core.ToolCapabilities{ReadsWorkspace: true, WritesWorkspace: true})
	metadata.AlwaysVisible = advertise
	core.Register(c, core.Tool[preparedApplyPatch]{
		Spec: core.ToolSpec{
			Name:        "apply_patch",
			Description: "Apply reviewable workspace patches. Use Add File for new files, Update File for existing files, Delete File to remove files, and dry_run to validate without writing.",
			Example:     "{\"name\":\"apply_patch\",\"arguments\":{\"patch\":\"*** Begin Patch\\n*** Add File: hello.txt\\n+hello\\n*** Update File: note.txt\\n@@\\n-old\\n+new\\n*** Delete File: obsolete.txt\\n*** End Patch\",\"dry_run\":false}}",
			Parameters:  core.SchemaFor[applyPatchArgs](),
			Metadata:    metadata,
		},
		Decode: func(call *core.ToolCall) (preparedApplyPatch, error) {
			return prepareApplyPatch(call, workspace)
		},
		Approve: func(ctx context.Context, plan preparedApplyPatch) core.ApprovalDecision {
			req := patchApprovalRequest(plan.changes, workspace, plan.patchText, plan.hunkCount, plan.args.DryRun)
			req.Confined, req.Writes, req.ReadOnly = true, !plan.args.DryRun, plan.args.DryRun
			req.Paths = core.MapSlice(plan.changes, func(c plannedFileChange) string { return c.displayPath })
			req.Protected = !plan.args.DryRun && slices.ContainsFunc(plan.changes, func(c plannedFileChange) bool { return workspace.IsProtected(c.absPath) })
			// A dry run writes nothing; it only prompts when a hidden or
			// sensitive path adds a finding beyond the baseline reason.
			if plan.args.DryRun && req.Reason == baselinePatchReason {
				return core.Automatic(req)
			}
			return core.Ask(req)
		},
		Approver: core.ApproverFrom(c),
		AuditFailure: func(ctx context.Context, plan preparedApplyPatch, err error) {
			dispatchApplyPatchAudit(dispatcher, ctx, plan.ops, plan.args.DryRun, plan.hunkCount, "rejected", err)
		},
		Execute: func(ctx context.Context, plan preparedApplyPatch) (string, error) {
			return runPreparedApplyPatch(dispatcher, ctx, plan)
		},
	})
}

func prepareApplyPatch(call *core.ToolCall, workspace workspace.Policy) (preparedApplyPatch, error) {
	args, err := core.DecodeOptionalToolArgs[applyPatchArgs](call)
	if err != nil {
		return preparedApplyPatch{}, fmt.Errorf("parse apply_patch arguments: %w", err)
	}

	patchText := strings.ReplaceAll(args.Patch, "\r\n", "\n")
	if strings.TrimSpace(patchText) == "" {
		return preparedApplyPatch{}, fmt.Errorf("patch is required")
	}
	if len(patchText) > constants.ApplyPatchMaxBytes {
		return preparedApplyPatch{}, fmt.Errorf("patch exceeds max_patch_bytes (%d)", constants.ApplyPatchMaxBytes)
	}

	ops, hunkCount, err := parseApplyPatchText(patchText)
	if err != nil {
		return preparedApplyPatch{}, err
	}
	changes, err := planPatchChanges(ops, workspace)
	if err != nil {
		return preparedApplyPatch{}, err
	}
	return preparedApplyPatch{args: args, patchText: patchText, ops: ops, hunkCount: hunkCount, changes: changes}, nil
}

func runPreparedApplyPatch(dispatcher events.Dispatcher, ctx context.Context, plan preparedApplyPatch) (string, error) {
	if !plan.args.DryRun {
		if err := commitPatchChanges(plan.changes); err != nil {
			dispatchApplyPatchAudit(dispatcher, ctx, plan.ops, plan.args.DryRun, plan.hunkCount, "failed", err)
			return "", err
		}
	}

	resp := applyPatchResponse{ChangedFiles: core.MapSlice(plan.changes, changedFileResult), Hunks: plan.hunkCount, DryRun: plan.args.DryRun}
	data, err := core.MarshalResult("apply_patch", resp)
	if err != nil {
		return "", err
	}
	dispatchApplyPatchAudit(dispatcher, ctx, plan.ops, plan.args.DryRun, plan.hunkCount, "applied", nil)
	return data, nil
}

// changedFileResult renders one planned change for the tool result.
func changedFileResult(change plannedFileChange) applyPatchChangedFile {
	item := applyPatchChangedFile{Path: change.displayPath, Action: change.action}
	if change.existed {
		item.PreviousSHA256 = core.HashHex(change.original)
	}
	if change.action != constants.ApplyPatchActionDelete {
		item.NewSHA256 = core.HashHex(change.updated)
	}
	return item
}

func parseApplyPatchText(text string) ([]applyPatchOp, int, error) {
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || !strings.HasPrefix(strings.TrimSpace(lines[0]), constants.ApplyPatchBeginMarker) {
		return nil, 0, fmt.Errorf("patch must start with %s%s", constants.ApplyPatchBeginMarker, patchFormatReminder)
	}

	var ops []applyPatchOp
	hunkCount := 0
	for i := 1; i < len(lines); {
		if isEndMarker(lines[i]) {
			if i != len(lines)-1 && (i != len(lines)-2 || lines[len(lines)-1] != "") {
				return nil, 0, fmt.Errorf("unexpected content after %s", constants.ApplyPatchEndMarker)
			}
			return ops, hunkCount, nil
		}
		op, next, count, err := parsePatchOp(lines, i)
		if err != nil {
			return nil, 0, err
		}
		ops = append(ops, op)
		i = next
		hunkCount += count
	}

	// A patch whose sections all parsed but whose terminator is missing is
	// taken as complete: the marker adds nothing once every section has been
	// read, and rejecting it sent models into a rewrite loop. A patch that
	// parsed no section at all is still an error.
	if len(ops) > 0 {
		return ops, hunkCount, nil
	}
	return nil, 0, fmt.Errorf("patch is missing %s%s", constants.ApplyPatchEndMarker, patchFormatReminder)
}

// isEndMarker matches the terminator with trailing junk tolerated — models
// emit `*** End Patch"` or a stray backslash from their own JSON encoding.
func isEndMarker(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), constants.ApplyPatchEndMarker)
}

func parsePatchOp(lines []string, start int) (applyPatchOp, int, int, error) {
	line := lines[start]
	switch {
	case strings.HasPrefix(line, "*** Add File: "):
		op, next, err := parseAddFileOp(lines, start)
		return op, next, 1, err
	case strings.HasPrefix(line, "*** Delete File: "):
		path := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))
		return applyPatchOp{Action: constants.ApplyPatchActionDelete, Path: path}, start + 1, 0, nil
	case strings.HasPrefix(line, "*** Update File: "):
		return parseUpdateFileOp(lines, start)
	case strings.HasPrefix(line, "@@"):
		// Observed: models open with hunks and no file header, meaning "the
		// file I was editing". The target cannot be guessed safely; say
		// exactly what is missing instead of the generic parse error.
		return applyPatchOp{}, start, 0, fmt.Errorf("hunk found before any file header; start the section with %q naming the file this hunk belongs to%s", "*** Update File: <path>", patchFormatReminder)
	default:
		return applyPatchOp{}, start, 0, fmt.Errorf("unexpected patch line %q; every file section must start with %q, %q or %q%s", line, "*** Add File: <path>", "*** Update File: <path>", "*** Delete File: <path>", patchFormatReminder)
	}
}

// patchFormatReminder is appended to parse errors: a model that guesses the
// envelope wrong keeps guessing unless the error shows the shape it needs
// (observed in benchmark runs, where one run spent 18 turns on malformed
// patches). Kept to one compact example.
const patchFormatReminder = "\nexpected format:\n" +
	"*** Begin Patch\n" +
	"*** Update File: path/to/file\n" +
	"@@\n" +
	" unchanged context line\n" +
	"-removed line\n" +
	"+added line\n" +
	"*** End Patch"

// parseAddFileOp reads an Add File section. The "+" prefix is ceremony for an
// add — every line is added either way — so each body line contributes its
// content with one leading "+" removed if present. That accepts the fully
// prefixed body, the verbatim body, and the mixed body models actually emit
// (observed: a 9B model prefixing the first lines and forgetting the rest,
// which used to fail the whole patch). The cost is a content line legitimately
// starting with "+", which loses that character; an Update File hunk states
// its lines exactly and is the way to write one.
func parseAddFileOp(lines []string, start int) (applyPatchOp, int, error) {
	path := strings.TrimSpace(strings.TrimPrefix(lines[start], "*** Add File: "))
	end := start + 1
	for end < len(lines) && !strings.HasPrefix(lines[end], "*** ") {
		end++
	}
	content := core.MapSlice(lines[start+1:end], func(line string) string { return strings.TrimPrefix(line, "+") })
	return applyPatchOp{Action: constants.ApplyPatchActionCreate, Path: path, Hunks: []patchHunk{{Lines: []patchLine{{Kind: '+', Text: strings.Join(content, "\n")}}}}}, end, nil
}

func parseUpdateFileOp(lines []string, start int) (applyPatchOp, int, int, error) {
	path := strings.TrimSpace(strings.TrimPrefix(lines[start], "*** Update File: "))
	i := start + 1
	var hunks []patchHunk
	for i < len(lines) && !strings.HasPrefix(lines[i], "*** ") {
		// The @@ header is optional: it carries no information this parser uses
		// (hunks are located by context match), and a body line where it should
		// be is unambiguously the start of an implicit single hunk.
		body := i
		if strings.HasPrefix(lines[i], "@@") {
			body = i + 1
		}
		hunk, next := parseUpdateHunk(lines, body)
		hunks = append(hunks, hunk)
		i = next
	}
	if len(hunks) == 0 {
		return applyPatchOp{}, i, 0, fmt.Errorf("update file %q has no hunks", path)
	}
	return applyPatchOp{Action: constants.ApplyPatchActionUpdate, Path: path, Hunks: hunks}, i, len(hunks), nil
}

func parseUpdateHunk(lines []string, start int) (patchHunk, int) {
	i := start
	var hunk patchHunk
	for i < len(lines) && !strings.HasPrefix(lines[i], "@@") && !strings.HasPrefix(lines[i], "*** ") {
		hunk.Lines = append(hunk.Lines, parsePatchLine(lines[i]))
		i++
	}
	return hunk, i
}

// parsePatchLine tolerates a missing leading marker: models routinely emit
// context lines verbatim ("class Matrix:" instead of " class Matrix:"), and
// rejecting the whole patch for it caused rewrite loops (10 of 13 failures in
// one instrumented run). A line starting with none of " ", "+", "-" is taken
// as context with its full text; a wrong guess still has to survive the
// context match, which emits the file's own lines, so it cannot corrupt.
func parsePatchLine(raw string) patchLine {
	if raw == "" {
		return patchLine{Kind: ' ', Text: ""}
	}
	if raw[0] == ' ' || raw[0] == '+' || raw[0] == '-' {
		return patchLine{Kind: raw[0], Text: raw[1:]}
	}
	return patchLine{Kind: ' ', Text: raw}
}

func planPatchChanges(ops []applyPatchOp, workspace workspace.Policy) ([]plannedFileChange, error) {
	changes := make([]plannedFileChange, 0, len(ops))
	// Models write "*** Delete File: x" + "*** Add File: x" in one patch to
	// mean "rewrite this file"; a path deleted earlier in the patch is absent
	// for a later add. Ops commit in order, so the delete's backup still
	// makes the pair atomic under rollback.
	deleted := make(map[string]bool)
	for _, op := range ops {
		change, err := planPatchChange(op, workspace, deleted)
		if err != nil {
			return nil, err
		}
		if op.Action == constants.ApplyPatchActionDelete {
			deleted[change.absPath] = true
		}
		changes = append(changes, change)
	}
	return changes, nil
}

func planPatchChange(op applyPatchOp, workspace workspace.Policy, deleted map[string]bool) (plannedFileChange, error) {
	resolved, err := workspace.ResolveWrite(op.Path)
	if err != nil {
		return plannedFileChange{}, err
	}
	info, statErr := os.Stat(resolved)
	exists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return plannedFileChange{}, fmt.Errorf("stat %s: %w", op.Path, statErr)
	}
	change := plannedFileChange{displayPath: workspace.RelDisplayPath(resolved, op.Path), absPath: resolved, action: op.Action, existed: exists, mode: 0o644}
	if exists {
		change.mode = info.Mode().Perm()
		content, err := os.ReadFile(resolved)
		if err != nil {
			return plannedFileChange{}, fmt.Errorf("read %s: %w", op.Path, err)
		}
		change.original = string(content)
		change.originalSHA256 = core.HashHex(change.original)
	}
	if err := applyActionToChange(op, exists && !deleted[resolved], &change); err != nil {
		return plannedFileChange{}, err
	}
	return change, nil
}

func applyActionToChange(op applyPatchOp, exists bool, change *plannedFileChange) error {
	switch op.Action {
	case constants.ApplyPatchActionCreate:
		if exists {
			return fmt.Errorf("cannot create %s: file already exists; use *** Update File: %s to change it", op.Path, op.Path)
		}
		change.updated = op.Hunks[0].Lines[0].Text
	case constants.ApplyPatchActionDelete:
		if !exists {
			return fmt.Errorf("cannot delete %s: file does not exist", op.Path)
		}
	case constants.ApplyPatchActionUpdate:
		if !exists {
			return fmt.Errorf("cannot update %s: file does not exist; use *** Add File: %s to create it", op.Path, op.Path)
		}
		updated, err := applyHunks(change.original, op.Hunks)
		if err != nil {
			return fmt.Errorf("update %s: %w", op.Path, err)
		}
		change.updated = updated
	default:
		return fmt.Errorf("unsupported patch action %q", op.Action)
	}
	return nil
}

// baselinePatchReason is the reason every patch approval carries; hidden or
// sensitive path findings are appended after it.
const baselinePatchReason = "workspace write"

// patchApprovalRequest always asks: apply_patch declares approval policy
// "ask" (write_workspace), and nothing upstream enforces Metadata.Policy for
// native tools — the Approve hook is the sole gate. Hidden/sensitive paths
// only add extra detail to the reason.
func patchApprovalRequest(changes []plannedFileChange, workspace workspace.Policy, patchText string, hunkCount int, dryRun bool) approval.Request {
	sensitiveGlobs := constants.DefaultApplyPatchSensitivePathGlobs()
	reasons := []string{baselinePatchReason}
	for _, change := range changes {
		if !workspace.AllowsHiddenPath(change.displayPath) {
			reasons = append(reasons, fmt.Sprintf("hidden path %q", change.displayPath))
		}
		if matchesAnyGlob(change.displayPath, sensitiveGlobs) {
			reasons = append(reasons, fmt.Sprintf("sensitive path %q", change.displayPath))
		}
	}
	return approval.Request{
		Tool: "apply_patch", Reason: strings.Join(reasons, ", "), Tier: "write_workspace",
		Summary: fmt.Sprintf("Apply patch touching %d file(s)", len(changes)),
		Details: fmt.Sprintf("Dry run: %v\nHunks: %d\nChanged files:\n%s\n\nPatch:\n%s", dryRun, hunkCount, describePatchChanges(changes), patchText),
		Subject: patchSubject(changes),
		// The subject is a path list: a standing grant must match every
		// touched path, not the joined string as one blob.
		SubjectIsPathList: true,
	}
}

// patchSubject is the matchable argument string for a patch approval: the
// workspace-relative paths the patch touches, space separated, so a standing
// grant like `docs/*` can cover it. Nothing model-authored beyond the paths
// themselves goes here.
func patchSubject(changes []plannedFileChange) string {
	return strings.Join(core.MapSlice(changes, func(c plannedFileChange) string { return c.displayPath }), " ")
}

func describePatchChanges(changes []plannedFileChange) string {
	return strings.Join(core.MapSlice(changes, func(c plannedFileChange) string {
		return fmt.Sprintf("- %s %s", c.action, c.displayPath)
	}), "\n")
}

func applyHunks(original string, hunks []patchHunk) (string, error) {
	// prepareApplyPatch normalizes CRLF in the patch text, so a CRLF file has
	// to be matched with the carriage return ignored (locateHunk's second
	// rung): otherwise context copied verbatim out of read_file (which is
	// byte-faithful, terminators included) can never match, and no model can
	// write a hunk that applies. Every existing line keeps the ending it
	// already had — rewriting the whole file to one ending turned a one-line
	// edit into a whole-file diff on a file with a single stray CRLF line —
	// and an added line takes the ending the file mostly uses.
	lines := splitLines(original)
	addedEOL := addedLineEnding(original)
	out := make([]string, 0, len(lines))
	cursor := 0

	for hunkIdx, hunk := range hunks {
		oldSeq := hunkOldSeq(hunk)
		pos, err := locateHunk(lines, cursor, oldSeq, hunkIdx)
		if err != nil {
			return "", err
		}
		out = append(out, lines[cursor:pos]...)

		idx := pos
		for _, line := range hunk.Lines {
			switch line.Kind {
			case '+':
				out = append(out, line.Text+addedEOL)
			case ' ':
				out = append(out, lines[idx])
				idx++
			default: // '-' — parsePatchLine admits no other kind
				idx++
			}
		}
		cursor = idx
	}

	out = append(out, lines[cursor:]...)
	return joinLines(out, strings.HasSuffix(original, "\n")), nil
}

// addedLineEnding reports the carriage return an inserted line needs to carry
// the ending the file mostly uses. Lines are joined on "\n" and keep their own
// "\r", so this is the only place a new ending is chosen.
func addedLineEnding(content string) string {
	return strings.TrimSuffix(workspace.DominantEOL(content), "\n")
}

func hunkOldSeq(hunk patchHunk) []string {
	seq := make([]string, 0, len(hunk.Lines))
	for _, line := range hunk.Lines {
		if line.Kind == ' ' || line.Kind == '-' {
			seq = append(seq, line.Text)
		}
	}
	return seq
}

// hunkMatchLimit caps enumeration; 2 proves ambiguity, a few more make a better message.
const hunkMatchLimit = 4

// locateHunk finds the unique position of a hunk's old sequence. Exact match
// first; when nothing matches exactly, a whitespace-insensitive pass runs,
// accepted only when unique — models copy context with the wrong indentation
// (or hit an invisible trailing-space line) far more often than a file has two
// regions equal modulo whitespace. Context and deleted lines are consumed from
// the file itself, so a fuzzy match never writes the model's whitespace back.
func locateHunk(lines []string, cursor int, seq []string, hunkIdx int) (int, error) {
	matches := findHunkMatches(lines, cursor, seq, exactLineMatch)
	if len(matches) == 0 {
		for _, eq := range []func(a, b string) bool{crLineMatch, looseLineMatch} {
			if fuzzy := findHunkMatches(lines, cursor, seq, eq); len(fuzzy) == 1 {
				return fuzzy[0], nil
			}
		}
	}
	switch {
	case len(matches) == 1:
		return matches[0], nil
	case len(matches) > 1:
		return 0, fmt.Errorf("hunk %d: context matches %d locations (first at lines %d and %d); add more context lines to disambiguate", hunkIdx+1, len(matches), matches[0]+1, matches[1]+1)
	}
	return 0, fmt.Errorf("hunk %d: %s", hunkIdx+1, describeHunkMismatch(lines, cursor, seq))
}

func exactLineMatch(a, b string) bool { return a == b }

// crLineMatch ignores the carriage return a CRLF file's lines carry, so a hunk
// written against the normalized patch text matches without also tolerating
// the indentation drift looseLineMatch admits.
func crLineMatch(a, b string) bool { return strings.TrimSuffix(a, "\r") == strings.TrimSuffix(b, "\r") }

func looseLineMatch(a, b string) bool { return strings.TrimSpace(a) == strings.TrimSpace(b) }

func findHunkMatches(lines []string, cursor int, seq []string, eq func(a, b string) bool) []int {
	if len(seq) == 0 {
		return []int{cursor}
	}
	var found []int
	for start := cursor; start+len(seq) <= len(lines); start++ {
		if slices.EqualFunc(lines[start:start+len(seq)], seq, eq) {
			found = append(found, start)
			if len(found) == hunkMatchLimit {
				break
			}
		}
	}
	return found
}

func describeHunkMismatch(lines []string, cursor int, seq []string) string {
	if earlier := findHunkMatches(lines, 0, seq, exactLineMatch); len(earlier) > 0 && earlier[0] < cursor {
		return fmt.Sprintf("context matches at line %d, before the previous hunk ended at line %d; hunks must be ordered by file position", earlier[0]+1, cursor+1)
	}
	start, matched := closestCandidate(lines, cursor, seq)
	if matched == 0 {
		return fmt.Sprintf("context not found in lines %d-%d; no line matches %q", cursor+1, len(lines), seq[0])
	}
	if start+matched >= len(lines) {
		return fmt.Sprintf("context not found; matched %d of %d lines at line %d, then the file ended", matched, len(seq), start+1)
	}
	want, got := seq[matched], lines[start+matched]
	return fmt.Sprintf("context not found; closest match at line %d matched %d of %d lines, then diverged%s\n  expected: %q\n  actual:   %q", start+1, matched, len(seq), whitespaceHint(want, got), want, got)
}

// closestCandidate returns the search position with the longest matching prefix of seq.
func closestCandidate(lines []string, cursor int, seq []string) (int, int) {
	bestStart, bestLen := cursor, 0
	for start := cursor; start < len(lines); start++ {
		n := 0
		for n < len(seq) && start+n < len(lines) && lines[start+n] == seq[n] {
			n++
		}
		if n > bestLen {
			bestStart, bestLen = start, n
		}
	}
	return bestStart, bestLen
}

func whitespaceHint(want, got string) string {
	if strings.Join(strings.Fields(want), " ") == strings.Join(strings.Fields(got), " ") {
		return " (whitespace/indentation only)"
	}
	return ""
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

func joinLines(lines []string, trailingNewline bool) string {
	if len(lines) == 0 {
		return ""
	}
	text := strings.Join(lines, "\n")
	if trailingNewline {
		return text + "\n"
	}
	return text
}

func commitPatchChanges(changes []plannedFileChange) error {
	patchCommitMu.Lock()
	defer patchCommitMu.Unlock()
	if err := validatePlannedChanges(changes); err != nil {
		return err
	}

	states := make([]rollbackState, 0, len(changes))
	for _, change := range changes {
		state := rollbackState{change: change}
		if err := applyFileChange(change, &state); err != nil {
			rollbackPatchChanges(states)
			return err
		}
		states = append(states, state)
	}

	for _, state := range states {
		if state.backup != "" {
			_ = os.Remove(state.backup)
		}
	}
	return nil
}

func validatePlannedChanges(changes []plannedFileChange) error {
	for _, change := range changes {
		content, err := os.ReadFile(change.absPath)
		switch {
		case !change.existed && os.IsNotExist(err):
			continue
		case err != nil:
			return fmt.Errorf("validate %s before commit: %w", change.displayPath, err)
		case !change.existed:
			return fmt.Errorf("cannot commit %s: file changed since patch was prepared", change.displayPath)
		case core.HashHex(string(content)) != change.originalSHA256:
			return fmt.Errorf("cannot commit %s: file changed since patch was prepared", change.displayPath)
		}
	}
	return nil
}

func applyFileChange(change plannedFileChange, state *rollbackState) error {
	if err := os.MkdirAll(filepath.Dir(change.absPath), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", change.displayPath, err)
	}

	create := change.action == constants.ApplyPatchActionCreate
	remove := change.action == constants.ApplyPatchActionDelete
	if !create && !remove && change.action != constants.ApplyPatchActionUpdate {
		return fmt.Errorf("unsupported change action %q", change.action)
	}
	// Everything but a create moves the current file aside first, so a failed
	// write (or a later change in the same patch) can put it back.
	if !create {
		backup, err := moveToBackup(change.absPath)
		if err != nil {
			return fmt.Errorf("%s %s: %w", pickWord(remove, "delete", "backup"), change.displayPath, err)
		}
		state.backup = backup
	}
	if !remove {
		if err := fsutil.WriteFileAtomic(change.absPath, []byte(change.updated), change.mode); err != nil {
			if state.backup != "" {
				_ = os.Rename(state.backup, change.absPath)
			}
			return fmt.Errorf("%s %s: %w", pickWord(create, "create", "write"), change.displayPath, err)
		}
		state.created = create
	}
	state.applied = true
	return nil
}

// pickWord names the failing step for an error message.
func pickWord(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}

func rollbackPatchChanges(states []rollbackState) {
	for i := len(states) - 1; i >= 0; i-- {
		state := states[i]
		if !state.applied {
			continue
		}
		switch {
		case state.created:
			_ = os.Remove(state.change.absPath)
		case state.backup != "":
			_ = os.Remove(state.change.absPath)
			_ = os.Rename(state.backup, state.change.absPath)
		}
	}
}

func moveToBackup(path string) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".invokr-backup-*")
	if err != nil {
		return "", err
	}
	backup := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(backup)
		return "", err
	}
	if err := os.Remove(backup); err != nil {
		return "", err
	}
	if err := os.Rename(path, backup); err != nil {
		return "", err
	}
	return backup, nil
}

func dispatchApplyPatchAudit(dispatcher events.Dispatcher, ctx context.Context, ops []applyPatchOp, dryRun bool, hunks int, outcome string, err error) {
	paths := make([]string, 0, len(ops))
	for _, op := range ops {
		if strings.TrimSpace(op.Path) != "" {
			paths = append(paths, op.Path)
		}
	}
	core.DispatchAudit(ctx, dispatcher, &events.AuditPayload{Action: "apply_patch", Tool: "apply_patch", Paths: paths, DryRun: dryRun, Hunks: hunks, Outcome: outcome}, err)
}

func matchesAnyGlob(path string, globs []string) bool {
	path = filepath.ToSlash(path)
	return slices.ContainsFunc(globs, func(glob string) bool {
		return workspace.MatchGlob(strings.TrimSpace(glob), path)
	})
}
