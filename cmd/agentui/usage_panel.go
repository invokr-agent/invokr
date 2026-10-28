package agentui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/platform/turn"
	"github.com/invokr-agent/invokr/surface/ui"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	humanize "github.com/dustin/go-humanize"
)

const usageRowBarScale = 16

// usageHint names the panel's own keys for the row the cursor sits on: enter
// only means anything on a row that holds others, so every other row says select
// alone, and an expanded row collapses rather than expands. The overlay paints
// esc close itself, so the hint never repeats it.
func usageHint(groups []usageGroup, selected int, expanded bool) string {
	gi, ri, ok := usageRowAt(groups, selected)
	if !ok {
		return "↑↓ select"
	}
	if len(groups[gi].Rows[ri].Members) == 0 {
		return "↑↓ select"
	}
	verb := "expand"
	if expanded {
		verb = "collapse"
	}
	return "↑↓ select · enter " + verb
}

// usageRow is one row of the /usage panel: a labelled token count, plus the
// rows it stands for when it is a folded "other" row.
type usageRow struct {
	Label   string
	Tokens  int64
	Members []usageMember
	// folded marks the row as one that swallowed other rows, whose member names
	// the row itself prints; a merged row keeps its members for enter alone.
	Folded bool
}

type usageMember struct {
	Label  string
	Tokens int64
}

// usageGroup is one of the panel's two groups: the fixed cost of every request
// (setup) or the transcript /compact reclaims (history). Style paints both the
// group's stacked-bar cells and its row bars.
type usageGroup struct {
	Name   string
	Style  lipgloss.Style
	Rows   []usageRow
	Tokens int64
}

// usagePanel is the /usage panel's state: the groups and rows to draw, the row
// the cursor sits on, and whether a folded "other" row is expanded in place.
type usagePanel struct {
	Groups         []usageGroup
	Breakdown      turn.ContextBreakdown
	Selected       int
	Expanded       bool
	Open           bool
	Usage          turn.TokenUsage
	CompactPercent int
}

// usageRowName maps a breakdown category onto the panel row it belongs to. The
// fixed prompt sections merge into one instructions row; the transcript kinds
// take the plain words the design uses.
func usageRowName(category string) (group, label string) {
	named := map[string][]string{
		"tool specs":         {"setup", "tool specs"},
		"tool schemas":       {"setup", "tool schemas"},
		"user messages":      {"history", "you"},
		"assistant messages": {"history", "assistant"},
		"tool calls":         {"history", "tool calls"},
		"tool results":       {"history", "tool results"},
		"completion":         {"history", "last reply"},
		"prompt":             {"unsplit", "prompt"},
		"unattributed":       {"history", "unattributed"},
	}
	if pair, ok := named[category]; ok {
		return pair[0], pair[1]
	}
	return "setup", "instructions"
}

// foldRows merges the rows under 3% of usage into one other row, but only when
// two or more qualify and at least one row of the group stays unfolded: when
// every row is small the fold leaves other as the group's only line, hiding
// every label the panel meant to show, so those rows keep their own lines.
func foldRows(rows []usageRow, used int64) []usageRow {
	cut := used * 3 / 100
	keep, small := []usageRow{}, []usageMember{}
	for _, row := range rows {
		if row.Tokens >= cut {
			// rows at or over the cut keep their own line
			keep = append(keep, row)
			continue
		}
		small = append(small, usageMember{Label: row.Label, Tokens: row.Tokens})
	}
	if len(small) < 2 || len(keep) == 0 {
		// a lone small row says more as itself, and folding every row leaves other
		// alone with nothing to compare it against
		return rows
	}
	merged := usageRow{Label: "other", Members: small, Folded: true}
	for _, member := range small {
		merged.Tokens += member.Tokens
	}
	return append(keep, merged)
}

// usageGroups lays the breakdown's categories out into the panel's two groups,
// each sorted by tokens and folded where the small rows qualify.
func usageGroups(breakdown turn.ContextBreakdown) []usageGroup {
	// unsplit holds a prompt total no breakdown attributed: it is setup and
	// history together, so neither group may claim it. It is empty, and so
	// drawn not at all, whenever the session can attribute its usage.
	groups := []usageGroup{{Name: "setup", Style: ui.Invokr.Info}, {Name: "history", Style: ui.Invokr.Warn},
		{Name: "unsplit", Style: ui.Invokr.Muted}}
	var used int64
	for _, category := range breakdown.Categories {
		name, label := usageRowName(category.Name)
		group := &groups[slices.IndexFunc(groups, func(g usageGroup) bool { return g.Name == name })]
		group.Tokens += category.Tokens
		used += category.Tokens
		if at := slices.IndexFunc(group.Rows, func(row usageRow) bool { return row.Label == label }); at >= 0 {
			// a row that takes a second category keeps both as members, so enter
			// can list what it merged: the prompt sections share instructions.
			group.Rows[at].Tokens += category.Tokens
			group.Rows[at].Members = append(group.Rows[at].Members, usageMember{Label: category.Name, Tokens: category.Tokens})
		} else {
			group.Rows = append(group.Rows, usageRow{Label: label, Tokens: category.Tokens,
				Members: []usageMember{{Label: category.Name, Tokens: category.Tokens}}})
		}
	}
	for index := range groups {
		for ri := range groups[index].Rows {
			row := &groups[index].Rows[ri]
			if len(row.Members) < 2 {
				// one category is the row itself, and there is nothing to expand
				row.Members = []usageMember{}
				continue
			}
			slices.SortStableFunc(row.Members, func(a, b usageMember) int { return cmp.Compare(b.Tokens, a.Tokens) })
		}
		slices.SortStableFunc(groups[index].Rows, func(a, b usageRow) int { return cmp.Compare(b.Tokens, a.Tokens) })
		groups[index].Rows = foldRows(groups[index].Rows, used)
	}
	return groups
}

// usageBarCells divides the stacked bar between the two groups with largest-
// remainder rounding, so the painted cells equal round(used/window*40) and a
// group too small to reach a cell takes none.
func usageBarCells(groups []usageGroup, window int64) []int {
	cells := make([]int, len(groups))
	if window <= 0 {
		return cells
	}
	remainders := make([]int64, len(groups))
	order := make([]int, len(groups))
	var used int64
	painted := 0
	for index, group := range groups {
		used += group.Tokens
		numerator := group.Tokens * int64(maxContextBarWidth)
		cells[index] = min(int(numerator/window), maxContextBarWidth)
		remainders[index] = numerator % window
		order[index] = index
		painted += cells[index]
	}
	target := int(float64(used)/float64(window)*float64(maxContextBarWidth) + 0.5)
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(remainders[b], remainders[a]) })
	for _, index := range order {
		if painted >= target {
			break
		}
		cells[index]++
		painted++
	}
	return cells
}

// usageEighths draws a row's bar on eighth blocks against the largest row in
// the panel, keeping a non-zero row visible down to a single eighth.
func usageEighths(tokens, largest int64) string {
	if tokens <= 0 {
		return ""
	}
	if largest <= 0 {
		return "▏"
	}
	scaled := float64(tokens) / float64(largest) * float64(usageRowBarScale)
	full, part := int(scaled), int((scaled-float64(int(scaled)))*8)
	marks := []rune(" ▏▎▍▌▋▊▉█")
	if full == 0 && part == 0 {
		return "▏"
	}
	bar := strings.Repeat("█", full)
	if part > 0 {
		bar += string(marks[part])
	}
	return bar
}

// usageHeaderLine draws a group's own line: swatch, name, tokens and its share
// of the window. Group headers are not selectable.
func usageHeaderLine(group usageGroup, window int64) string {
	swatch := group.Style.Render("■ ")
	return swatch + padRight(group.Name, 22) + padLeft(humanize.Comma(group.Tokens), 9) +
		padLeft(usagePercent(group.Tokens, window), 7)
}

func usagePercent(tokens, window int64) string {
	if window <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.1f%%", float64(tokens)/float64(window)*100)
}

// padRight and padLeft line the panel's columns out on display width: the
// bars and glyphs are multi-byte, so a byte count would skew every column.
func padRight(s string, width int) string {
	return s + strings.Repeat(" ", max(0, width-lipgloss.Width(s)))
}
func padLeft(s string, width int) string {
	return strings.Repeat(" ", max(0, width-lipgloss.Width(s))) + s
}

// usageBarLine paints the stacked bar: one colour per group, the group the
// cursor sits in full colour and the other muted, free cells muted.
func usageBarLine(groups []usageGroup, cells []int, selectedGroup string) string {
	var bar strings.Builder
	var painted int
	for index, group := range groups {
		if cells[index] <= 0 {
			continue
		}
		style := group.Style
		if selectedGroup != "" && group.Name != selectedGroup {
			style = ui.Invokr.Muted
		}
		bar.WriteString(style.Render(strings.Repeat("█", cells[index])))
		painted += cells[index]
	}
	free := max(0, maxContextBarWidth-painted)
	bar.WriteString(ui.Invokr.Muted.Render(strings.Repeat("░", free)))
	return bar.String()
}

// usageLabelWidth sizes the label column to the longest label the panel draws:
// every row, plus the selected row's members when it is expanded, never below
// the 12 columns the plain rows already have. A folded row's member names trail
// after the bar, so they ask for no column room.
func usageLabelWidth(groups []usageGroup, selected int, expanded bool) int {
	var index int
	widest := 12
	for _, group := range groups {
		// a group with nothing in it draws no rows, so it asks for no room
		if group.Tokens <= 0 {
			continue
		}
		for _, row := range group.Rows {
			widest = max(widest, lipgloss.Width(row.Label))
			if index == selected && expanded {
				// only the row the cursor sits on lists its members
				for _, member := range row.Members {
					// a member sits two cells deeper, so its label needs those two
					// columns of the shared field to hold its tokens on one column
					widest = max(widest, lipgloss.Width(member.Label)+2)
				}
			}
			index++
		}
	}
	return widest
}

// usageRowLine draws one row: cursor, label, tokens, its bar, and for a folded
// row the list of rows it stands for. A merged row such as instructions holds
// its members for enter to expand in place, so it prints no trailing names. indent
// is the row's own marker width and labelWidth the panel's label column: a member
// sits two columns deeper and gives those two back from its pad, so every token
// and bar column lines up on rows and members alike. Only the selected row keeps
// its label and bar in full colour.
func usageRowLine(row usageRow, largest int64, style lipgloss.Style, selected bool,
	indent, labelWidth int) string {
	bar := usageEighths(row.Tokens, largest)
	painted := style
	if !selected {
		painted = ui.Invokr.Muted
	}
	marker := strings.Repeat(" ", indent)
	if selected {
		// the cursor sits two cells in from the label's own edge
		marker = strings.Repeat(" ", indent-4) + style.Render("  ▸ ")
	}
	// a member's two extra indent cells come out of its pad, not the column
	label := padRight(row.Label, labelWidth-indent+4)
	if selected {
		label = style.Bold(true).Render(label)
	} else {
		label = ui.Invokr.Muted.Render(label)
	}
	line := marker + label + padLeft(humanize.Comma(row.Tokens), 8) + "  " +
		painted.Render(padRight(bar, usageRowBarScale))
	if row.Folded && len(row.Members) > 0 {
		names := make([]string, len(row.Members))
		for index, member := range row.Members {
			names[index] = member.Label
		}
		line += ui.Invokr.Muted.Render("  " + strings.Join(names, ", "))
	}
	return line
}

func usageFreeLine(free, window int64) string {
	return ui.Invokr.Muted.Render("□ " + padRight("free", 22) + padLeft(humanize.Comma(free), 9) +
		padLeft(usagePercent(free, window), 7))
}

func usageTotalsLine(used, window int64, compactPercent int) string {
	if window <= 0 {
		return "window size unknown"
	}
	return humanize.Comma(used) + " / " + humanize.Comma(window) + " (" +
		fmt.Sprintf("%.0f%%", float64(used)/float64(window)*100) + ") · compacts at " +
		fmt.Sprintf("%d%%", compactPercent)
}

// usageReasoningParts returns the last-turn and session reasoning lines as
// plain text, empty when there is nothing to say: reasoning is not carried
// into later prompts, so it is reported apart from the bar.
func usageReasoningParts(usage turn.TokenUsage, breakdown turn.ContextBreakdown) (last, session string) {
	mark := ""
	if breakdown.ReasoningEstimated {
		mark = "~"
	}
	// completion_tokens counts the thinking text, so the estimate is sized off
	// that total and stays under it; the min is a backstop for a breakdown handed
	// over with an estimate that still outruns the provider's counts, which would
	// print a share over 100% and an answer of nothing.
	latest := breakdown.ReasoningTokens
	total := breakdown.SessionReasoningTokens
	if breakdown.ReasoningEstimated {
		latest = min(latest, usage.LatestCompletionTokens)
		total = min(total, usage.SessionCompletionTokens)
	}
	var lastParts []string
	if latest > 0 {
		lastParts = append(lastParts, "reasoning "+mark+formatTokensCompact(latest))
	}
	if usage.LatestCompletionTokens > 0 {
		answer := max(0, usage.LatestCompletionTokens-latest)
		if !breakdown.ReasoningEstimated || answer > 0 {
			lastParts = append(lastParts, "answer "+formatTokensCompact(answer))
		}
	}
	if usage.LatestPromptTokens > 0 && usage.LatestCachedPromptTokens > 0 {
		lastParts = append(lastParts, "cached "+fmt.Sprintf("%.0f%%", float64(usage.LatestCachedPromptTokens)/float64(usage.LatestPromptTokens)*100))
	}
	if len(lastParts) > 0 {
		last = strings.Join(lastParts, " · ")
	}
	if total > 0 {
		session = "reasoning " + mark + formatTokensCompact(total)
		if usage.SessionCompletionTokens > 0 {
			share := float64(total) / float64(usage.SessionCompletionTokens) * 100
			session += " of " + formatTokensCompact(usage.SessionCompletionTokens) + " (" + fmt.Sprintf("%.0f%%", share) + ")"
		}
		session += " · only history shrinks on /compact"
	}
	return
}

// usageLargest finds the largest row, which the row bars scale against and the
// cursor opens on, and its index among the selectable rows.
func usageLargest(groups []usageGroup) (largest int64, at int) {
	index := 0
	for _, group := range groups {
		for _, row := range group.Rows {
			if row.Tokens > largest {
				largest, at = row.Tokens, index
			}
			index++
		}
	}
	return largest, at
}

// usagePanelLines draws the panel body: the estimated note, the stacked bar,
// the totals, the two groups with their rows, the free row and the reasoning
// lines. selected -1 marks no row, which leaves every bar in full colour.
func usagePanelLines(usage turn.TokenUsage, breakdown turn.ContextBreakdown, groups []usageGroup,
	selected int, expanded bool, compactPercent int) []string {
	window := usage.ContextWindowTokens
	var used int64
	for _, group := range groups {
		used += group.Tokens
	}
	selectedGroup := ""
	if gi, _, ok := usageRowAt(groups, selected); ok {
		selectedGroup = groups[gi].Name
	}
	var lines []string
	if breakdown.Estimated {
		lines = append(lines, ui.Invokr.Muted.Render("(estimated — no provider usage reported yet)"))
	}
	lines = append(lines, usageBarLine(groups, usageBarCells(groups, window), selectedGroup))
	lines = append(lines, usageTotalsLine(used, window, compactPercent))
	if window > 0 && float64(used)/float64(window)*100 > float64(constants.ContextWarningPercent) {
		lines = append(lines, ui.Invokr.Warn.Render("warning at "+fmt.Sprintf("%d%%", constants.ContextWarningPercent)))
	}
	lines = append(lines, "")
	largest, _ := usageLargest(groups)
	lines = append(lines, usageGroupLines(groups, window, largest, selected, expanded)...)
	lines = append(lines, usageFreeLine(max(0, window-used), window))
	return append(lines, usagePanelReasoningLines(usage, breakdown)...)
}

// usagePlainText is plain-mode /usage: the panel's rows with no row selected,
// stripped of colour, without the key hint, plus the /compact estimate.
func usagePlainText(usage turn.TokenUsage, breakdown turn.ContextBreakdown, groups []usageGroup,
	reclaimable int64, compactPercent int) string {
	lines := usagePanelLines(usage, breakdown, groups, -1, false, compactPercent)
	for index, line := range lines {
		lines[index] = strings.TrimRight(ansi.Strip(line), " ")
	}
	if reclaimable > 0 {
		lines = append(lines, "/compact reclaims an estimated "+formatTokensCompact(reclaimable))
	}
	return strings.Join(lines, "\n")
}

func (m *model) openUsagePanel(usage turn.TokenUsage, breakdown turn.ContextBreakdown, compactPercent int) {
	groups := usageGroups(breakdown)
	_, start := usageLargest(groups)
	m.usagePanel = usagePanel{Groups: groups, Selected: start,
		Usage: usage, Breakdown: breakdown, CompactPercent: compactPercent, Open: true}
	m.renderUsagePanel()
}

// renderUsagePanel republishes the panel over the finished frame.
func (m *model) renderUsagePanel() {
	lines := usagePanelLines(m.usagePanel.Usage, m.usagePanel.Breakdown, m.usagePanel.Groups,
		m.usagePanel.Selected, m.usagePanel.Expanded, m.usagePanel.CompactPercent)
	lines = append(lines, "", ui.Invokr.Muted.Render(
		usageHint(m.usagePanel.Groups, m.usagePanel.Selected, m.usagePanel.Expanded)))
	m.overlay.Open("usage", strings.Join(lines, "\n"))
}

// usageRowAt indexes a selectable row: which group, and which row inside it;
// ok is false past the last row or for a negative index.
func usageRowAt(groups []usageGroup, selected int) (group, row int, ok bool) {
	seen := 0
	for gi, g := range groups {
		if selected >= seen && selected < seen+len(g.Rows) {
			return gi, selected - seen, true
		}
		seen += len(g.Rows)
	}
	return 0, 0, false
}

// updateUsagePanelKey owns the panel's keys while it is open: the cursor moves
// on up/down (and j/k), enter expands or collapses a row that holds others, and
// esc collapses one or closes the panel.
func (m *model) updateUsagePanelKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	p := &m.usagePanel
	if !p.Open || !m.overlay.IsOpen() || m.overlay.Title() != "usage" {
		return nil, false
	}
	switch msg.String() {
	case "esc":
		if p.Expanded {
			p.Expanded = false
		} else {
			m.overlay.Close()
			p.Open = false
		}
	case "up", "k":
		m.moveUsageCursor(-1)
	case "down", "j":
		m.moveUsageCursor(1)
	case "enter", "space":
		m.toggleUsageRow()
	default:
		return nil, true
	}
	if m.overlay.IsOpen() {
		m.renderUsagePanel()
	}
	return nil, true
}

func (m *model) moveUsageCursor(step int) {
	p := &m.usagePanel
	if _, _, ok := usageRowAt(p.Groups, p.Selected+step); ok {
		p.Selected += step
		p.Expanded = false
	}
}

// toggleUsageRow opens or collapses a row that holds others in place: a merged
// row such as instructions lists its prompt sections, a folded row lists the
// rows it swallowed. On any other row there is nothing to expand, so it says
// nothing.
func (m *model) toggleUsageRow() {
	p := &m.usagePanel
	if gi, ri, ok := usageRowAt(p.Groups, p.Selected); ok && len(p.Groups[gi].Rows[ri].Members) > 0 {
		p.Expanded = !p.Expanded
	}
}

// usageBreakdownOf is the panel's view of a session that cannot attribute usage:
// the prompt/completion split, restated as rows so both paths share one renderer.
func usageBreakdownOf(usage turn.TokenUsage, breakdown turn.ContextBreakdown) turn.ContextBreakdown {
	if breakdown.Available && len(breakdown.Categories) > 0 {
		return breakdown
	}
	return turn.ContextBreakdown{Available: true, WindowTokens: usage.ContextWindowTokens,
		Categories: []turn.ContextCategory{
			{Name: "prompt", Tokens: usage.LatestPromptTokens},
			{Name: "completion", Tokens: usage.LatestCompletionTokens},
		}}
}

// usageGroupLines draws the two groups with their headers, rows and any
// expanded members, in display order.
func usageGroupLines(groups []usageGroup, window, largest int64, selected int, expanded bool) []string {
	var lines []string
	// one label column for the whole body, so a long member label such as
	// response contract leaves every token and bar column where it already is
	labelWidth := usageLabelWidth(groups, selected, expanded)
	index := 0
	for _, group := range groups {
		if group.Tokens <= 0 {
			continue
		}
		lines = append(lines, usageHeaderLine(group, window))
		for _, row := range group.Rows {
			on := index == selected
			lines = append(lines, usageRowLine(row, largest, group.Style, on, 4, labelWidth))
			if on && expanded {
				for _, member := range row.Members {
					lines = append(lines, usageRowLine(usageRow{Label: member.Label, Tokens: member.Tokens},
						largest, group.Style, false, 6, labelWidth))
				}
			}
			index++
		}
	}
	return lines
}

// usagePanelReasoningLines restyles the reasoning parts in violet: the last
// turn's thinking and answer sit apart from the session total, which is the
// only part /compact cannot reclaim because it never enters later prompts.
// labelWidth is every block label's width, so a value's second line starts where
// the value does: the session's prompt total sits as line two under the session
// label rather than between the blocks, where it read as the current prompt.
func usagePanelReasoningLines(usage turn.TokenUsage, breakdown turn.ContextBreakdown) []string {
	// every block label is the same width, so a value's second line lines up on
	// the column the values start
	const labelWidth = 11
	last, session := usageReasoningParts(usage, breakdown)
	// the prompt total is the session's, so it rides as line two under the
	// session label; with no session line it keeps its own label instead of
	// sitting under nothing
	prompt := usagePromptSplit(usage)
	var lines []string
	if last != "" {
		// violet: the last turn's thinking and answer sit apart from the session total
		lines = append(lines, ui.Invokr.Muted.Render("last turn  ")+ui.Invokr.Info.Render(last))
	}
	if session == "" {
		if prompt != "" {
			lines = append(lines, ui.Invokr.Muted.Render("prompt     ")+prompt)
		}
		return lines
	}
	head, rest, found := strings.Cut(session, " · ")
	if !found {
		rest = ""
	}
	// violet: the last turn's thinking and answer sit apart from the session total
	lines = append(lines, ui.Invokr.Muted.Render("session    ")+ui.Invokr.Info.Render(head)+
		ui.Invokr.Muted.Render(" · "+rest))
	if prompt != "" {
		lines = append(lines, ui.Invokr.Muted.Render(strings.Repeat(" ", labelWidth)+"prompt ")+prompt)
	}
	return lines
}

// usagePromptSplit splits the session's prompt total into what the prompt cache
// served and what was evaluated fresh (issue #359), so /usage shows what the
// cache saved. A provider that reports no cached tokens gets no line.
func usagePromptSplit(usage turn.TokenUsage) string {
	cached := usage.SessionCachedPromptTokens
	if cached <= 0 || usage.SessionPromptTokens <= 0 {
		return ""
	}
	return formatTokensCompact(usage.SessionPromptTokens) + " · cached " + formatTokensCompact(cached) +
		fmt.Sprintf(" (%.0f%%)", float64(cached)/float64(usage.SessionPromptTokens)*100) +
		" · uncached " + formatTokensCompact(usage.SessionPromptTokens-cached)
}
