package agentui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	humanize "github.com/dustin/go-humanize"

	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/surface/ui"

	"charm.land/lipgloss/v2"

	tea "charm.land/bubbletea/v2"
)

const usageRowBarScale = 16

// usageRow is one row of the /usage panel: a labelled token count, plus the
// rows it stands for when it is a folded "other" row.
type usageRow struct {
	Label   string
	Tokens  int64
	Members []usageMember
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
		"unattributed":       {"history", "unattributed"},
	}
	if pair, ok := named[category]; ok {
		return pair[0], pair[1]
	}
	return "setup", "instructions"
}

// foldRows merges the rows under 3% of usage into one other row, but only when
// two or more qualify: a single small row says more as itself.
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
	if len(small) < 2 {
		return rows
	}
	merged := usageRow{Label: "other", Members: small}
	for _, member := range small {
		merged.Tokens += member.Tokens
	}
	return append(keep, merged)
}

// usageGroups lays the breakdown's categories out into the panel's two groups,
// each sorted by tokens and folded where the small rows qualify.
func usageGroups(breakdown turn.ContextBreakdown) []usageGroup {
	setup, history := map[string]int64{}, map[string]int64{}
	counts := map[string]map[string]int64{"setup": setup, "history": history}
	for _, category := range breakdown.Categories {
		group, label := usageRowName(category.Name)
		counts[group][label] += category.Tokens
	}
	groups := []usageGroup{
		usageGroup{Name: "setup", Style: ui.Invokr.Info, Rows: rowsFor(setup, breakdown.Categories)},
		usageGroup{Name: "history", Style: ui.Invokr.Warn, Rows: rowsFor(history, breakdown.Categories)},
	}
	for index := range groups {
		for _, row := range groups[index].Rows {
			groups[index].Tokens += row.Tokens
		}
	}
	return groups
}

// rowsFor sorts one group's counts into rows, largest first, then folds.
func rowsFor(counts map[string]int64, categories []turn.ContextCategory) []usageRow {
	var used int64
	for _, category := range categories {
		used += category.Tokens
	}
	rows := []usageRow{}
	for label, tokens := range counts {
		rows = append(rows, usageRow{Label: label, Tokens: tokens})
	}
	slices.SortFunc(rows, func(a, b usageRow) int { return cmp.Compare(b.Tokens, a.Tokens) })
	return foldRows(rows, used)
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
		cells[index] = min(int(numerator / window), maxContextBarWidth)
		remainders[index] = numerator % window
		order[index] = index
		painted += cells[index]
	}
	target := int(float64(used) / float64(window) * float64(maxContextBarWidth) + 0.5)
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
	full, part := int(scaled), int((scaled - float64(int(scaled))) * 8)
	marks := " ▏▎▍▌▋▊▉█"
	if full == 0 && part == 0 {
		return "▏"
	}
	bar := strings.Repeat("█", full)
	if part > 0 {
		bar += marks[part : part+1]
	}
	return bar
}

// usageHeaderLine draws a group's own line: swatch, name, tokens and its share
// of the window. Group headers are not selectable.
func usageHeaderLine(group usageGroup, window int64) string {
	swatch := group.Style.Render("■ ")
	return swatch + padRight(group.Name, 22) + padLeft(humanize.Comma(group.Tokens), 8) +
		padLeft(usagePercent(group.Tokens, window), 7)
}

// usagePercent formats a group or row's share of the window.
func usagePercent(tokens, window int64) string {
	if window <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.1f%%", float64(tokens)/float64(window)*100)
}

// padRight and padLeft line the panel's columns out on display text.
func padRight(s string, width int) string { return s + strings.Repeat(" ", max(0, width-len(s))) }
func padLeft(s string, width int) string { return strings.Repeat(" ", max(0, width-len(s))) + s }

// memberNames lists the rows a folded other row stands for, for its trailing
// text.
func memberNames(members []usageMember) []string {
	names := make([]string, len(members))
	for index, member := range members {
		names[index] = member.Label
	}
	return names
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

// usageSelectedGroup names the group the cursor sits in, so the stacked bar
// can keep that group in full colour.
func usageSelectedGroup(groups []usageGroup, selected int) string {
	index := 0
	for _, group := range groups {
		if selected >= index && selected < index+len(group.Rows) {
			return group.Name
		}
		index += len(group.Rows)
	}
	return ""
}

// usageRowLine draws one row: cursor, label, tokens, its bar, and for a folded
// row the list of rows it stands for. Only the selected row keeps its label and
// bar in full colour.
func usageRowLine(row usageRow, largest int64, style lipgloss.Style, selected bool) string {
	bar := usageEighths(row.Tokens, largest)
	painted := style
	if !selected {
		painted = ui.Invokr.Muted
	}
	marker := "    "
	if selected {
		marker = style.Render("  ▸ ")
	}
	label := padLeft(row.Label, 14)
	if selected {
		label = style.Bold(true).Render(label)
	} else {
		label = ui.Invokr.Muted.Render(label)
	}
	line := marker + label + " " + padLeft(humanize.Comma(row.Tokens), 8) + "  " +
		painted.Render(padLeft(bar, usageRowBarScale+1))
	if len(row.Members) > 0 {
		line += ui.Invokr.Muted.Render("  "+strings.Join(memberNames(row.Members), ", "))
	}
	return line
}

// usageFreeLine closes the legend with the room the window still has.
func usageFreeLine(free, window int64) string {
	return ui.Invokr.Muted.Render("□ "+padRight("free", 22)+padLeft(humanize.Comma(free), 8)+
		padLeft(usagePercent(free, window), 7))
}

// usageTotalsLine reports used over window and the compaction trigger.
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
	var lastParts []string
	if breakdown.ReasoningTokens > 0 {
		lastParts = append(lastParts, "reasoning "+mark+formatTokensCompact(breakdown.ReasoningTokens))
	}
	if usage.LatestCompletionTokens > 0 {
		answer := max(0, usage.LatestCompletionTokens-breakdown.ReasoningTokens)
		lastParts = append(lastParts, "answer "+formatTokensCompact(answer))
	}
	if usage.LatestPromptTokens > 0 && usage.LatestCachedPromptTokens > 0 {
		lastParts = append(lastParts, "cached "+fmt.Sprintf("%.0f%%", float64(usage.LatestCachedPromptTokens)/float64(usage.LatestPromptTokens)*100))
	}
	if len(lastParts) > 0 {
		last = strings.Join(lastParts, " · ")
	}
	if breakdown.SessionReasoningTokens > 0 {
		session = "reasoning "+mark+formatTokensCompact(breakdown.SessionReasoningTokens)
		if usage.SessionCompletionTokens > 0 {
			share := float64(breakdown.SessionReasoningTokens)/float64(usage.SessionCompletionTokens)*100
			session += " of "+formatTokensCompact(usage.SessionCompletionTokens)+" ("+fmt.Sprintf("%.0f%%", share)+")"
		}
		session += " · only history shrinks on /compact"
	}
	return
}

// usageBarPlain draws the stacked bar without colour, for plain-mode /usage.
func usageBarPlain(groups []usageGroup, window int64) string {
	var painted int
	var bar strings.Builder
	for _, count := range usageBarCells(groups, window) {
		painted += count
		bar.WriteString(strings.Repeat("█", count))
	}
	bar.WriteString(strings.Repeat("░", max(0, maxContextBarWidth-painted)))
	return bar.String()
}

// usagePanelUsed sums the rows the panel paints, which is what the totals
// line and the bar scale to.
func usagePanelUsed(groups []usageGroup) int64 {
	var used int64
	for _, group := range groups {
		used += group.Tokens
	}
	return used
}

// usageLargestRow is the row the row bars scale against: the largest row wins
// a full sixteen cells.
func usageLargestRow(groups []usageGroup) int64 {
	largest := int64(0)
	for _, group := range groups {
		for _, row := range group.Rows {
			if row.Tokens > largest {
				largest = row.Tokens
			}
		}
	}
	return largest
}

// usagePanelRowCount counts the selectable rows; group headers are not
// selectable.
func usagePanelRowCount(groups []usageGroup) int {
	count := 0
	for _, group := range groups {
		count += len(group.Rows)
	}
	return count
}

// usagePanelStart picks the row the cursor opens on: the largest row overall,
// in whichever group holds it.
func usagePanelStart(groups []usageGroup) int {
	largest, at, index := int64(0), 0, 0
	for _, group := range groups {
		for _, row := range group.Rows {
			if row.Tokens > largest {
				largest, at = row.Tokens, index
			}
			index++
		}
	}
	return at
}

// usagePanelLines draws the panel body: the estimated note, the stacked bar,
// the totals, the two groups with their rows, the free row, the reasoning
// lines and the key hint.
func usagePanelLines(usage turn.TokenUsage, breakdown turn.ContextBreakdown, groups []usageGroup,
	selected int, expanded bool, compactPercent int) string {
	window := usage.ContextWindowTokens
	used := usagePanelUsed(groups)
	var lines []string
	if breakdown.Estimated {
		lines = append(lines, ui.Invokr.Muted.Render("(estimated — no provider usage reported yet)"))
	}
	lines = append(lines, usageBarLine(groups, usageBarCells(groups, window), usageSelectedGroup(groups, selected)))
	lines = append(lines, usageTotalsLine(used, window, compactPercent))
	if window > 0 && float64(used)/float64(window)*100 > float64(constants.ContextWarningPercent) {
		lines = append(lines, ui.Invokr.Warn.Render("warning at "+fmt.Sprintf("%d%%", constants.ContextWarningPercent)))
	}
	lines = append(lines, "")
	lines = append(lines, usageGroupLines(groups, window, usageLargestRow(groups), selected, expanded)...)
	lines = append(lines, usageFreeLine(max(0, window-used), window))
	lines = append(lines, usagePanelReasoningLines(usage, breakdown)...)
	lines = append(lines, "", ui.Invokr.Muted.Render("↑↓ select · enter expand other · esc close"))
	return strings.Join(lines, "\n")
}

// usagePlainText is plain-mode /usage: the same rows as text, without colour,
// selection or the key hint.
func usagePlainText(usage turn.TokenUsage, breakdown turn.ContextBreakdown, groups []usageGroup,
	reclaimable int64, compactPercent int) string {
	window := usage.ContextWindowTokens
	used := usagePanelUsed(groups)
	var lines []string
	if breakdown.Estimated {
		lines = append(lines, "(estimated — no provider usage reported yet)")
	}
	lines = append(lines, usageBarPlain(groups, window), usageTotalsLine(used, window, compactPercent))
	largest := usageLargestRow(groups)
	for _, group := range groups {
		if group.Tokens <= 0 {
			continue
		}
		lines = append(lines, "■ "+padRight(group.Name, 22)+padLeft(humanize.Comma(group.Tokens), 8)+
			padLeft(usagePercent(group.Tokens, window), 7))
		for _, row := range group.Rows {
			note := ""
			if len(row.Members) > 0 {
				note = "  "+strings.Join(memberNames(row.Members), ", ")
			}
			lines = append(lines, "    "+padLeft(row.Label, 14)+" "+padLeft(humanize.Comma(row.Tokens), 8)+
				"  "+padLeft(usageEighths(row.Tokens, largest), usageRowBarScale+1)+note)
		}
	}
	lines = append(lines, "□ "+padRight("free", 22)+padLeft(humanize.Comma(max(0, window-used)), 8)+
		padLeft(usagePercent(max(0, window-used), window), 7))
	last, session := usageReasoningParts(usage, breakdown)
	if last != "" {
		lines = append(lines, "last turn  "+last)
	}
	if session != "" {
		lines = append(lines, "session    "+session)
	}
	if reclaimable > 0 {
		lines = append(lines, "/compact reclaims an estimated "+formatTokensCompact(reclaimable))
	}
	return strings.Join(lines, "\n")
}

// openUsagePanel builds the panel from a breakdown and shows it, starting the
// cursor on the largest row overall.
func (m *model) openUsagePanel(usage turn.TokenUsage, breakdown turn.ContextBreakdown, compactPercent int) {
	m.usagePanel = usagePanel{Groups: usageGroups(breakdown), Selected: usagePanelStart(usageGroups(breakdown)),
		Usage: usage, Breakdown: breakdown, CompactPercent: compactPercent, Open: true}
	m.renderUsagePanel()
}

// renderUsagePanel republishes the panel over the finished frame.
func (m *model) renderUsagePanel() {
	m.overlay.Open("usage", usagePanelLines(m.usagePanel.Usage, m.usagePanel.Breakdown, m.usagePanel.Groups,
		m.usagePanel.Selected, m.usagePanel.Expanded, m.usagePanel.CompactPercent))
}

// usagePanelRow indexes the row the cursor sits on: which group, and which row
// inside it.
func (p *usagePanel) rowAt() (int, int) {
	seen := 0
	for gi, g := range p.Groups {
		if p.Selected < seen+len(g.Rows) {
			return gi, p.Selected-seen
		}
		seen += len(g.Rows)
	}
	return 0, 0
}

// updateUsagePanelKey owns the panel's keys while it is open: the cursor moves
// on up/down (and j/k), enter expands a folded row in place, and esc collapses
// one or closes the panel.
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

// moveUsageCursor steps the cursor over the rows, keeping it inside them.
func (m *model) moveUsageCursor(step int) {
	p := &m.usagePanel
	next := p.Selected+step
	if next < 0 || next >= usagePanelRowCount(p.Groups) {
		return
	}
	p.Selected = next
}

// toggleUsageRow opens or collapses a folded other row in place; on any other
// row there is nothing to expand, so it says nothing.
func (m *model) toggleUsageRow() {
	p := &m.usagePanel
	gi, ri := p.rowAt()
	if gi < 0 || gi >= len(p.Groups) || ri < 0 || ri >= len(p.Groups[gi].Rows) {
		return
	}
	if len(p.Groups[gi].Rows[ri].Members) == 0 {
		return
	}
	p.Expanded = !p.Expanded
}

// usageBreakdownOf is the panel's view of a session that cannot attribute usage:
// the prompt/completion split, restated as rows so both paths share one renderer.
func usageBreakdownOf(usage turn.TokenUsage, breakdown turn.ContextBreakdown) turn.ContextBreakdown {
	if breakdown.Available && len(breakdown.Categories) > 0 {
		return breakdown
	}
	return turn.ContextBreakdown{Available: true, WindowTokens: usage.ContextWindowTokens,
		Categories: []turn.ContextCategory{
			turn.ContextCategory{Name: "prompt", Tokens: usage.LatestPromptTokens},
			turn.ContextCategory{Name: "completion", Tokens: usage.LatestCompletionTokens},
		}}
}

// usageGroupLines draws the two groups with their headers, rows and any
// expanded members, in display order.
func usageGroupLines(groups []usageGroup, window, largest int64, selected int, expanded bool) []string {
	var lines []string
	index := 0
	for _, group := range groups {
		if group.Tokens <= 0 {
			continue
		}
		lines = append(lines, usageHeaderLine(group, window))
		for _, row := range group.Rows {
			on := index == selected
			lines = append(lines, usageRowLine(row, largest, group.Style, on))
			if on && expanded {
				for _, member := range row.Members {
					lines = append(lines, usageRowLine(usageRow{Label: member.Label, Tokens: member.Tokens},
						largest, group.Style, false))
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
func usagePanelReasoningLines(usage turn.TokenUsage, breakdown turn.ContextBreakdown) []string {
	last, session := usageReasoningParts(usage, breakdown)
	var lines []string
	if last != "" {
		lines = append(lines, ui.Invokr.Muted.Render("last turn  ")+ui.Invokr.Info.Render(last))
	}
	if session == "" {
		return lines
	}
	head, rest, found := strings.Cut(session, " · ")
	if !found {
		rest = ""
	}
	return append(lines, ui.Invokr.Muted.Render("session    ")+ui.Invokr.Info.Render(head)+
		ui.Invokr.Muted.Render(" · "+rest))
}
