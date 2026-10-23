package agentui

import (
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/invokr-ai/invokr/platform/turn"
)

// TestUsageEighthsIsValidUTF8 covers the byte-slice bug: the eighth blocks are
// multi-byte, so indexing the marks table by byte cut every glyph in half and
// painted invalid UTF-8 on the right border.
func TestUsageEighthsIsValidUTF8(t *testing.T) {
	for part := 1; part <= 7; part++ {
		for _, full := range []int{0, 1} {
			tokens := int64(part + full*8)
			bar := usageEighths(tokens, 128)
			if strings.ContainsRune(bar, '\uFFFD') {
				t.Fatalf("bar for part %d, full %d is not valid UTF-8: %q", part, full, bar)
			}
			marks := []rune(" ▏▎▍▌▋▊▉█")
			want := strings.Repeat("█", full) + string(marks[part])
			if bar != want {
				t.Fatalf("bar for part %d, full %d = %q, want %q", part, full, bar, want)
			}
		}
	}
}

// TestUsageReasoningPartsClampsEstimated covers the char estimate of the
// thinking text outrunning the provider's completion counts: the printed share
// must stay at or under 100%, and an answer of nothing is dropped rather than
// shown as "answer 0".
func TestUsageReasoningPartsClampsEstimated(t *testing.T) {
	usage := turn.TokenUsage{LatestCompletionTokens: 14, SessionCompletionTokens: 14}
	breakdown := turn.ContextBreakdown{ReasoningEstimated: true, ReasoningTokens: 36, SessionReasoningTokens: 36}
	last, session := usageReasoningParts(usage, breakdown)
	if !strings.Contains(last, "reasoning ~14") || strings.Contains(last, "answer") {
		t.Fatalf("last-turn line over an over-estimate = %q, want a clamp and no answer part", last)
	}
	if !strings.Contains(session, "reasoning ~14 of 14 (100%)") || strings.Contains(session, "257%") {
		t.Fatalf("session line over an over-estimate = %q", session)
	}
}

// TestUsagePanelHintOmitsEscClose: the overlay already paints its own esc close
// footer, so no per-row hint may say it a second time.
func TestUsagePanelHintOmitsEscClose(t *testing.T) {
	groups := []usageGroup{{Name: "history", Style: lipgloss.Style{}, Rows: []usageRow{
		{Label: "tool results", Tokens: 83790},
		{Label: "other", Tokens: 443, Members: []usageMember{{Label: "you", Tokens: 300},
			{Label: "last reply", Tokens: 143}}, Folded: true},
		}}}
	for _, expanded := range []bool{false, true} {
		if got := usageHint(groups, 1, expanded); strings.Contains(got, "esc close") {
			t.Fatalf("panel hint = %q, want it without a second esc close", got)
		}
	}
}

// TestUsagePlainText covers plain-mode /usage: the fallback split without a
// breakdown, the effective compaction trigger, and the /compact estimate that
// is omitted rather than advertised as zero.
func TestUsagePlainText(t *testing.T) {
	usage := turn.TokenUsage{Available: true, ContextWindowTokens: 61440, LatestPromptTokens: 12400, LatestCompletionTokens: 50,
		LatestCachedPromptTokens: 9920}
	breakdown := usageBreakdownOf(usage, turn.ContextBreakdown{})
	percent := compactionThreshold(nil, usage.ContextWindowTokens)
	got := usagePlainText(usage, breakdown, usageGroups(breakdown), 9400, percent)
	for _, want := range []string{"■ unsplit", "    prompt", "■ history", "last reply", "□ free", "compacts at 89%", "cached 80%",
		"/compact reclaims an estimated 9.4k"} {
		if !strings.Contains(got, want) {
			t.Fatalf("plain /usage missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "instructions") || strings.Contains(got, "\x1b") || strings.Contains(got, "▸") {
		t.Fatalf("plain /usage misfiled the prompt, kept colour or a cursor:\n%s", got)
	}
	if got := usagePlainText(usage, breakdown, usageGroups(breakdown), 0, percent); strings.Contains(got, "/compact reclaims") {
		t.Fatalf("plain /usage advertised a zero reclaim:\n%s", got)
	}
}

// TestUsageCursorCollapsesOther: leaving an expanded other row collapses it,
// so coming back finds it folded.
func TestUsageCursorCollapsesOther(t *testing.T) {
	m := &model{usagePanel: usagePanel{Expanded: true, Groups: []usageGroup{{Rows: []usageRow{{Label: "a"}, {Label: "other"}}}}, Selected: 1}}
	m.moveUsageCursor(-1)
	if m.usagePanel.Expanded || m.usagePanel.Selected != 0 {
		t.Fatalf("cursor move left the panel at row %d, expanded %v", m.usagePanel.Selected, m.usagePanel.Expanded)
	}
}

// TestUsagePanelMatchesSpecColumns renders the spec's example breakdown and
// pins the text columns to the spec's layout. The spec's sample bars are not
// drawn to its own 16-cell scale, so bars are checked for a shared start
// column instead of compared glyph for glyph.
func TestUsagePanelMatchesSpecColumns(t *testing.T) {
	setup := usageGroup{Name: "setup", Tokens: 6670, Rows: []usageRow{
		{Label: "tool schemas", Tokens: 4948}, {Label: "tool specs", Tokens: 906}, {Label: "instructions", Tokens: 816},
	}}
	history := usageGroup{Name: "history", Tokens: 106099, Rows: []usageRow{
		{Label: "tool results", Tokens: 83790}, {Label: "tool calls", Tokens: 16159}, {Label: "assistant", Tokens: 9002},
		{Label: "other", Tokens: 2443, Members: []usageMember{{Label: "you"}, {Label: "last reply"}}, Folded: true},
	}}
	groups := []usageGroup{setup, history}
	const window = 204800
	largest, start := usageLargest(groups)
	lines := usageGroupLines(groups, window, largest, start, false)
	lines = append(lines, usageFreeLine(92031, window))
	for index, line := range lines {
		lines[index] = ansi.Strip(line)
	}
	want := []string{
		"■ setup                     6,670   3.3%",
		"    tool schemas   4,948  ",
		"    tool specs       906  ",
		"    instructions     816  ",
		"■ history                 106,099  51.8%",
		"  ▸ tool results  83,790  ",
		"    tool calls    16,159  ",
		"    assistant      9,002  ",
		"    other          2,443  ",
		"□ free                     92,031  44.9%",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(want), strings.Join(lines, "\n"))
	}
	const barColumn = 26
	for index, line := range lines {
		if !strings.HasPrefix(line, want[index]) {
			t.Errorf("line %d = %q, want prefix %q", index, line, want[index])
		}
		if strings.HasPrefix(want[index], "    ") || strings.HasPrefix(want[index], "  ▸") {
			bar := []rune(line)[barColumn:]
			if len(bar) == 0 || bar[0] == ' ' {
				t.Errorf("line %d: bar does not start at column %d: %q", index, barColumn, line)
			}
		}
	}
	other := lines[8]
	if got := strings.Index(other, "you, last reply"); lipgloss.Width(other[:got]) != barColumn+usageRowBarScale+2 {
		t.Errorf("other's members start at column %d, want %d: %q", lipgloss.Width(other[:got]), barColumn+usageRowBarScale+2, other)
	}
}

// TestUsagePromptSplit covers issue #359: cached prompt tokens are shown apart
// from the uncached ones, and a provider that reports none gets no line.
func TestUsagePromptSplit(t *testing.T) {
	cached := turn.TokenUsage{SessionPromptTokens: 1_200_000, SessionCachedPromptTokens: 900_000}
	if got := usagePromptSplit(cached); got != "1200k · cached 900k (75%) · uncached 300k" {
		t.Fatalf("prompt split = %q", got)
	}
	if got := usagePromptSplit(turn.TokenUsage{SessionPromptTokens: 12000}); got != "" {
		t.Fatalf("uncached session got a prompt split: %q", got)
	}
}

// TestUsageUnsplitPromptStaysOutOfHistory: the fallback prompt total is setup
// and history together, so it must not count toward the history /compact
// reclaims.
func TestUsageUnsplitPromptStaysOutOfHistory(t *testing.T) {
	usage := turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: 400, LatestCompletionTokens: 50}
	for _, group := range usageGroups(usageBreakdownOf(usage, turn.ContextBreakdown{})) {
		if want := map[string]int64{"setup": 0, "history": 50, "unsplit": 400}[group.Name]; group.Tokens != want {
			t.Fatalf("%s holds %d tokens, want %d", group.Name, group.Tokens, want)
		}
	}
}

// TestUsageExpandedMembersKeepTheColumns: a member label longer than the plain
// 12 columns, such as response contract, widens the label column for every line
// the panel draws rather than pushing its own tokens and bar right. The token
// right edge and the bar start therefore sit on one column across rows and
// members alike, and a member sits two cells deeper than its rows.
func TestUsageExpandedMembersKeepTheColumns(t *testing.T) {
	breakdown := turn.ContextBreakdown{Categories: []turn.ContextCategory{
		{Name: "tool schemas", Tokens: 4948}, {Name: "tool specs", Tokens: 906},
		{Name: "persona", Tokens: 3100}, {Name: "response contract", Tokens: 2200},
		{Name: "tool results", Tokens: 83790},
	}}
	// instructions leads the setup rows at 5300 tokens, so the cursor opens on
	// it. response contract is 17 wide and a member sits two cells deeper, so an
	// expanded body's label column is 19. tool specs stays a row of its own: it is
	// the only row under the cut.
	groups := usageGroups(breakdown)
	largest, _ := usageLargest(groups)
	const window = 204800
	for _, expanded := range []bool{false, true} {
		// a folded body keeps the plain column, an expanded one takes the member's
		labelWidth := 12
		if expanded {
			// response contract's 17 plus the member's two-cell indent
			labelWidth = 19
		}
		// the label column opens 4 cells in and the token field is 8 wide, so the
		// last token digit ends one cell before the gap that opens the bar
		edge := 4 + labelWidth + 8 - 1
		start := edge + 3
		for _, line := range usageGroupLines(groups, window, largest, 0, expanded) {
			plain := ansi.Strip(line)
			// a group header and the free row keep their own columns, so only a
			// row or a member opens with the indent's blanks
			if !strings.HasPrefix(plain, " ") {
				continue
			}
			chars := []rune(plain)
			// the token field is right aligned, so its last digit ends the run on
			// one column for every row and member
			if !strings.Contains("0123456789", string(chars[edge])) || chars[edge+1] != ' ' {
				t.Fatalf("tokens of %q end off column %d", plain, edge)
			}
			// the bar opens three cells after the tokens, on one column too
			if chars[edge+2] != ' ' || !strings.Contains("█▏▎▍▌▋▊▉", string(chars[start])) {
				t.Fatalf("bar of %q starts off column %d", plain, start)
			}
			// a member sits two cells deeper than a row, so its label starts at 6
			// while its tokens still end at the same edge
			for _, member := range []string{"persona", "response contract"} {
				if at := strings.Index(plain, member); at >= 0 && at != 6 {
					t.Fatalf("member %s starts at %d, want 6: %q", member, at, plain)
				}
			}
			for _, row := range []string{"tool specs", "tool results"} {
				if at := strings.Index(plain, row); at >= 0 && at != 4 {
					t.Fatalf("row %s starts at %d, want 4: %q", row, at, plain)
				}
			}
		}
	}
}

// TestUsageInstructionsRowHoldsItsSections covers the merged instructions row:
// every unnamed prompt section becomes one of its members, listed largest first,
// so enter expands the sections in place. The row itself prints no trailing
// names, which only a folded other row does.
func TestUsageInstructionsRowHoldsItsSections(t *testing.T) {
	breakdown := turn.ContextBreakdown{Categories: []turn.ContextCategory{
		{Name: "tool schemas", Tokens: 4948}, {Name: "tool specs", Tokens: 906},
		{Name: "persona", Tokens: 2100}, {Name: "working rules", Tokens: 1400},
		{Name: "tool results", Tokens: 83790},
	}}
	// persona and working rules sit over the 3% fold cut together, so their row
	// survives as instructions rather than joining an other row.
	groups := usageGroups(breakdown)
	rows := groups[0].Rows
	if rows[1].Label != "instructions" || rows[1].Folded {
		t.Fatalf("setup row 1 = %q, want the merged instructions row", rows[1].Label)
	}
	members := rows[1].Members
	if len(members) != 2 || members[0].Label != "persona" || members[0].Tokens != 2100 ||
		members[1].Label != "working rules" || members[1].Tokens != 1400 {
		t.Fatalf("instructions merged %d members, want persona 2100 then working rules 1400", len(members))
	}
	const window = 204800
	largest, _ := usageLargest(groups)
	collapsed := usageGroupLines(groups, window, largest, 1, false)
	expanded := usageGroupLines(groups, window, largest, 1, true)
	// the folded row keeps its sections off the line, so the hint is the only
	// sign enter does anything here.
	for _, line := range collapsed {
		if strings.Contains(line, "persona") || strings.Contains(line, "working rules") {
			t.Fatalf("folded instructions spilled its sections:\n%s", strings.Join(collapsed, "\n"))
		}
	}
	if len(expanded) != len(collapsed)+2 {
		t.Fatalf("enter on instructions added %d lines, want 2:\n%s", len(expanded)-len(collapsed),
			strings.Join(expanded, "\n"))
	}
	joined := strings.Join(expanded, "\n")
	if !strings.Contains(joined, "persona") || !strings.Contains(joined, "working rules") {
		t.Fatalf("expanded instructions lists neither section:\n%s", joined)
	}
}

// TestUsageFoldKeepsASmallGroupWhole covers the short-session case: when every
// row in a group falls under the 3% cut, merging them left other as the group's
// only line, hiding the labels the panel meant to show. Those rows keep their
// own lines, while a group that still holds a row at or over the cut folds its
// small rows into other as before.
func TestUsageFoldKeepsASmallGroupWhole(t *testing.T) {
	// a short session: setup's 14,900 tokens set the 3% cut at 447 over the
	// 14,924 used, so the whole 24-token history sits under it.
	used := int64(14_924)
	whole := []usageRow{{Label: "last reply", Tokens: 10}, {Label: "you", Tokens: 8},
		{Label: "assistant", Tokens: 6}}
	if got := foldRows(whole, used); len(got) != len(whole) {
		t.Fatalf("a group of all-small rows folded to %d rows, want its 3 kept:\n%s",
			len(got), strings.Join(labels(got), " "))
	}
	// one row at or over the cut still folds the rest into other
	mixed := append(whole, usageRow{Label: "tool results", Tokens: 20_000})
	got := foldRows(mixed, used)
	last := got[len(got)-1]
	if len(got) != 2 || got[0].Label != "tool results" || last.Label != "other" || !last.Folded {
		t.Fatalf("a group over the cut folded to %d rows, want tool results then other:\n%s",
			len(got), strings.Join(labels(got), " "))
	}
	if last.Tokens != 24 || len(last.Members) != 3 {
		t.Fatalf("other holds %d tokens over %d members, want 24 over 3",
			last.Tokens, len(last.Members))
	}
	// the same rule through usageGroups: the short session's history draws its
	// three rows, no other, and keeps its 24-token total
	groups := usageGroups(turn.ContextBreakdown{Available: true, Categories: []turn.ContextCategory{
		{Name: "tool schemas", Tokens: 5000}, {Name: "tool specs", Tokens: 900},
		{Name: "persona", Tokens: 9000},
		{Name: "completion", Tokens: 10}, {Name: "user messages", Tokens: 8},
		{Name: "assistant messages", Tokens: 6},
	}})
	for _, group := range groups {
		if group.Name != "history" {
			continue
		}
		if group.Tokens != 24 || len(group.Rows) != 3 {
			t.Fatalf("short-session history holds %d tokens over %d rows, want 24 over 3:\n%s",
				group.Tokens, len(group.Rows), strings.Join(labels(group.Rows), " "))
		}
		for _, row := range group.Rows {
			if row.Folded || row.Label == "other" {
				t.Fatalf("short-session history row %q is the merged other", row.Label)
			}
		}
	}
}

// labels lists the rows a fold left behind, for the failures above.
func labels(rows []usageRow) []string {
	out := make([]string, len(rows))
	for index, row := range rows {
		out[index] = row.Label
	}
	return out
}

// TestUsageHintTracksTheSelectedRow: enter is offered only on a row that holds
// others, so a row that is one category says select alone, and an expanded row
// collapses rather than expands again.
func TestUsageHintTracksTheSelectedRow(t *testing.T) {
	groups := []usageGroup{{Name: "setup", Style: lipgloss.Style{}, Rows: []usageRow{
		{Label: "tool schemas", Tokens: 4948},
		{Label: "instructions", Tokens: 3500, Members: []usageMember{{Label: "persona", Tokens: 2100},
			{Label: "working rules", Tokens: 1400}}},
		{Label: "tool specs", Tokens: 906},
	}}}
	want := map[int][]string{0: {"↑↓ select"}, 1: {"↑↓ select · enter expand", "↑↓ select · enter collapse"},
		2: {"↑↓ select"}}
	for _, selected := range []int{0, 1, 2} {
		for _, expanded := range []bool{false, true} {
			if selected != 1 && expanded {
				continue
			}
			got := usageHint(groups, selected, expanded)
			if !slices.Contains(want[selected], got) {
				t.Fatalf("hint over row %d expanded %v = %q", selected, expanded, got)
			}
			if selected != 1 && strings.Contains(got, "enter") {
				t.Fatalf("row %d offers enter, want select alone: %q", selected, got)
			}
		}
	}
}

// TestUsagePromptSplitSitsInTheSessionBlock covers the audit's reading of the
// prompt line: "prompt 13.4k · cached 4.4k (33%) · uncached 9k" is the session
// total, but it sat between the last-turn and session lines so it read as the
// current prompt. It now rides as line two under the session label, held to the
// column the values start, in the panel and in plain mode alike.
func TestUsagePromptSplitSitsInTheSessionBlock(t *testing.T) {
	usage := turn.TokenUsage{Available: true, ContextWindowTokens: 200_000,
		LatestPromptTokens: 13_000, LatestCompletionTokens: 400, LatestCachedPromptTokens: 4_000,
		SessionPromptTokens: 13_400, SessionCachedPromptTokens: 4_400, SessionCompletionTokens: 900}
	breakdown := turn.ContextBreakdown{Available: true, ReasoningEstimated: true,
		ReasoningTokens: 300, SessionReasoningTokens: 600}
	lines := usagePanelReasoningLines(usage, breakdown)
	if len(lines) != 3 {
		t.Fatalf("reasoning block drew %d lines, want last turn, session and its prompt line", len(lines))
	}
	const valueIndent = 11
	// the block's labels all hold that width, so the value lines share one column
	for _, label := range []string{"last turn  ", "session    "} {
		if lipgloss.Width(label) != valueIndent {
			t.Fatalf("label %q is %d wide, want %d", label, lipgloss.Width(label), valueIndent)
		}
	}
	promptLine := ansi.Strip(lines[2])
	if !strings.HasPrefix(promptLine, strings.Repeat(" ", valueIndent)+"prompt ") {
		t.Fatalf("prompt line = %q, want it indented to the values under the session label", promptLine)
	}
	if !strings.Contains(promptLine, "13.4k · cached 4.4k (33%) · uncached 9k") {
		t.Fatalf("prompt line = %q, want the session's prompt split", promptLine)
	}
	// the session line still leads its own prompt line, and the prompt split
	// stays out of the last-turn line
	if !strings.Contains(ansi.Strip(lines[1]), "session") || strings.Contains(ansi.Strip(lines[0]), "prompt") {
		t.Fatalf("reasoning block order wrong:\n%s", strings.Join(lines, "\n"))
	}
	// plain mode keeps the same lines, stripped of colour
	group := usageGroup{Name: "history", Style: lipgloss.Style{}, Rows: []usageRow{
		{Label: "tool results", Tokens: 83_790}}}
	plain := strings.Split(usagePlainText(usage, breakdown, []usageGroup{group}, 0, 89), "\n")
	if tail := plain[len(plain)-1]; !strings.HasPrefix(tail, strings.Repeat(" ", valueIndent)+"prompt ") {
		t.Fatalf("plain /usage prompt line = %q, want the same indent as the panel", tail)
	}
	// a session with no reasoning to say keeps the prompt split on its own label
	// rather than leaving it under a label that never opened
	silent := usagePanelReasoningLines(usage, turn.ContextBreakdown{Available: true})
	if len(silent) != 2 || !strings.HasPrefix(ansi.Strip(silent[1]), "prompt") {
		t.Fatalf("a session with no reasoning drew %d lines: %q", len(silent), strings.Join(silent, "\n"))
	}
}

// TestUsageAnswerSurvivesAnEstimatedThinkingTurn covers the audit's vanished
// answer: strata counts the thinking text inside completion_tokens, so the
// reasoning estimate is sized off that total and leaves the answer its own part
// rather than meeting the total and printing a 100% share.
func TestUsageAnswerSurvivesAnEstimatedThinkingTurn(t *testing.T) {
	usage := turn.TokenUsage{Available: true, LatestPromptTokens: 13_000, LatestCompletionTokens: 400,
		LatestCachedPromptTokens: 4_000, SessionCompletionTokens: 900}
	breakdown := turn.ContextBreakdown{Available: true, ReasoningEstimated: true,
		ReasoningTokens: 240, SessionReasoningTokens: 600}
	last, session := usageReasoningParts(usage, breakdown)
	if !strings.Contains(last, "reasoning ~240 · answer 160") {
		t.Fatalf("last-turn line over an estimated thinking turn = %q, want the answer part kept", last)
	}
	if !strings.Contains(session, "reasoning ~600 of 900 (67%)") || strings.Contains(session, "100%") {
		t.Fatalf("session line over an estimated thinking turn = %q", session)
	}
}
