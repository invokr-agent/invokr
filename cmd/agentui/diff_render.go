package agentui

import (
	"strings"

	"github.com/antonikliment/tuikit"
	"github.com/invokr-ai/invokr/surface/ui"
)

// Edit diffs render through tuikit's DiffView (chroma highlighting, intraline
// word emphasis, side-by-side when the pane is wide enough). DiffView takes
// before/after *contents*, and the transcript only carries the apply_patch
// patch text — so the fragments are reconstructed per file from the patch's
// context/-/+ lines. Caveat: the gutter line numbers count within the
// reconstructed fragment, not the real file; real positions would need the
// pre/post file contents plumbed through the tool event.

// patchSection is one file's worth of an apply_patch patch.
type patchSection struct {
	path          string
	before, after string
	deleted       bool
}

// maxDiffViewBytes is the largest patch fed through DiffView; anything bigger
// falls back to the plain colorized block rather than re-diffing megabytes
// every frame.
const maxDiffViewBytes = 128 * 1024

// hunkGapMark stands in for the lines a patch skipped between hunks. It is
// inserted into both fragments so DiffView shows it as one context row.
const hunkGapMark = "⋯"

// parsePatchSections reconstructs per-file before/after fragments from the
// apply_patch format. It returns nil when the text does not look like one.
func parsePatchSections(patch string) []patchSection {
	var sections []patchSection
	current := -1
	inHunk := false
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			sections = append(sections, patchSection{path: strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))})
			current, inHunk = len(sections)-1, true
		case strings.HasPrefix(line, "*** Update File: "):
			sections = append(sections, patchSection{path: strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))})
			current, inHunk = len(sections)-1, false
		case strings.HasPrefix(line, "*** Delete File: "):
			sections = append(sections, patchSection{path: strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: ")), deleted: true})
			current, inHunk = -1, false
		case strings.HasPrefix(line, "***"):
			current, inHunk = -1, false
		case current >= 0:
			appendPatchLine(&sections[current], line, &inHunk)
		}
	}
	return sections
}

func appendPatchLine(s *patchSection, line string, inHunk *bool) {
	if strings.HasPrefix(line, "@@") {
		if *inHunk {
			s.before += hunkGapMark + "\n"
			s.after += hunkGapMark + "\n"
		}
		*inHunk = true
		return
	}
	switch {
	case strings.HasPrefix(line, "+"):
		s.after += line[1:] + "\n"
	case strings.HasPrefix(line, "-"):
		s.before += line[1:] + "\n"
	default:
		s.before += strings.TrimPrefix(line, " ") + "\n"
		s.after += strings.TrimPrefix(line, " ") + "\n"
	}
}

// diffViewMemo keeps one DiffView per file section so its per-width render
// cache survives across frames (all setters no-op on unchanged values).
var diffViewMemo = map[string]*tuikit.DiffView{}

func diffViewFor(s patchSection) *tuikit.DiffView {
	key := s.path + "\x00" + s.before + "\x00" + s.after
	if dv, ok := diffViewMemo[key]; ok {
		return dv
	}
	if len(diffViewMemo) > 32 {
		clear(diffViewMemo)
	}
	dv := tuikit.NewDiffView(ui.Invokr.Palette).Before(s.path, s.before).After(s.path, s.after).Highlighter(tuikit.ChromaHighlighter(chromaTheme()))
	diffViewMemo[key] = dv
	return dv
}

// resetDiffViews drops the memo so a palette or syntax-theme change repaints.
func resetDiffViews() { clear(diffViewMemo) }

// parseUnifiedDiffSections reconstructs per-file before/after fragments from a
// unified diff (git diff / git show output). Returns nil when the text has no
// "diff --git" file headers. Header lines before the first file (git show's
// commit message) and per-file metadata (index, mode, ---/+++) are skipped;
// hunk lines share appendPatchLine with the apply_patch parser.
func parseUnifiedDiffSections(diff string) []patchSection {
	var sections []patchSection
	current := -1
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			sections = append(sections, patchSection{path: unifiedDiffPath(line)})
			current, inHunk = len(sections)-1, false
		case current < 0:
			continue
		case strings.HasPrefix(line, "deleted file mode"):
			sections[current].deleted = true
		case strings.HasPrefix(line, "@@"), inHunk && !strings.HasPrefix(line, "\\"):
			appendPatchLine(&sections[current], line, &inHunk)
		}
	}
	return sections
}

// unifiedDiffPath extracts the b-side path from a "diff --git a/x b/x" header.
// ponytail: splits on " b/", so a path containing that substring mis-parses;
// quoting/renames fall back to the raw header tail.
func unifiedDiffPath(header string) string {
	rest := strings.TrimPrefix(header, "diff --git ")
	if _, after, ok := strings.Cut(rest, " b/"); ok {
		return after
	}
	return rest
}

// renderGitDiff renders unified git diff output through DiffView, falling back
// to the plain colorized block when it does not parse or is too large.
func renderGitDiff(diff string, width, maxLines int) string {
	return renderDiffSections(parseUnifiedDiffSections(diff), diff, width, maxLines)
}

// renderPatchDiff renders an applied patch under its tool line. maxLines caps
// the whole block (0 = uncapped, the expanded form); a multi-file patch
// spends the budget top to bottom and folds the rest behind an indicator.
func renderPatchDiff(patch string, width, maxLines int) string {
	return renderDiffSections(parsePatchSections(patch), patch, width, maxLines)
}

func renderDiffSections(sections []patchSection, raw string, width, maxLines int) string {
	if len(sections) == 0 || len(raw) > maxDiffViewBytes {
		return renderDiffBlock(raw, width, maxLines)
	}
	var out []string
	budget := maxLines
	for i, s := range sections {
		if maxLines > 0 && budget <= 0 {
			out = append(out, foldIndicator(len(sections)-i, width)+" "+ui.Invokr.Muted.Render("(files)"))
			break
		}
		block := renderPatchSection(s, width, budget)
		out = append(out, block)
		budget -= strings.Count(block, "\n") + 1
	}
	return strings.Join(out, "\n")
}

func renderPatchSection(s patchSection, width, budget int) string {
	header := "    " + ui.Invokr.Frame.Render("── ") + ui.Invokr.Text.Render(s.path)
	if s.deleted {
		return header + " " + ui.Invokr.Danger.Render("(deleted)")
	}
	dv := diffViewFor(s)
	dv.MaxLines(max(0, budget-1))
	body := dv.RenderSplit(max(20, width-4))
	return header + "\n" + indentBlock(body, "    ")
}

// renderDiffBlock is the pre-DiffView plain fallback: colorized +/- lines,
// capped at maxLines (0 = uncapped). It also serves text that does not parse
// as an apply_patch patch.
func renderDiffBlock(diff string, width, maxLines int) string {
	return strings.Join(cappedLines(diff, width, maxLines, renderApprovalDetailLine), "\n")
}
