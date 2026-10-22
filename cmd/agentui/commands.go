package agentui

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/capability/approval"

	"github.com/antonikliment/tuikit/markdown"
	humanize "github.com/dustin/go-humanize"

	"github.com/antonikliment/tuikit"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/surface/ui"
	toolcore "github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/tool_search"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"
)

type CommandBackend interface {
	Execute(context.Context, string, []string) (string, error)
}

type commandHandler func(*model, string, []string) tea.Cmd
type plainHandler func(context.Context, CommandBackend, turn.Session, string, []string) (string, bool)

// voidCmd adapts a setter that only mutates the model, and textCmd one whose
// whole result is a single transcript line. Both are the shape most slash
// commands have, so they get an adapter each instead of a wrapper apiece.
func voidCmd(set func(*model, []string)) commandHandler {
	return func(m *model, _ string, args []string) tea.Cmd { set(m, args); return nil }
}

func textCmd(text func(*model, []string) string) commandHandler {
	return func(m *model, _ string, args []string) tea.Cmd { m.commandMessage(text(m, args)); return nil }
}

// sessionCmd and sessionText are the two adapters for a command that answers
// from the session alone, so the interactive and plain paths run one shared
// function instead of a near-copy each.
func sessionCmd(text func(turn.Session, []string) string) commandHandler {
	return func(m *model, _ string, args []string) tea.Cmd { m.commandMessage(text(m.session, args)); return nil }
}

func sessionText(text func(turn.Session, []string) string) plainHandler {
	return func(_ context.Context, _ CommandBackend, session turn.Session, _ string, args []string) (string, bool) {
		return text(session, args), true
	}
}

type commandSpec struct {
	name, usage, summary string
	handler              commandHandler
	plain                plainHandler
	needsBackend         bool
}

var commandRegistry []commandSpec

func init() {
	commandRegistry = []commandSpec{
		{name: "agents", usage: "/agents", summary: "list custom agents", handler: runBackendCommand, plain: runPlainBackend, needsBackend: true},
		{name: "approvals", usage: "/approvals [manual|auto|plan]", summary: "alias for /mode", handler: voidCmd((*model).setApprovalMode)},
		{name: "mode", usage: "/mode [manual|auto|plan]", summary: "show or set session mode (shift+tab cycles)", handler: voidCmd((*model).setApprovalMode)},
		{name: "publishing", usage: "/publishing [ask|allow]", summary: "publishing permission for this Auto session", handler: voidCmd((*model).setPublishing)},
		{name: "reads", usage: "/reads [workspace|ask]", summary: "reads outside the workspace: refuse, or ask each time (this session)", handler: voidCmd((*model).setReads)},
		{name: "clear", usage: "/clear", summary: "start a new session (keep saved history and memory)", handler: runClearCommand},
		{name: "compact", usage: "/compact", summary: "compact context before next turn", handler: sessionCmd(compactCommandText), plain: sessionText(compactCommandText)},
		{name: "context", usage: "/context", summary: "show token usage", handler: runContextCommand, plain: sessionText(plainContextText)},
		{name: "grants", usage: grantUsageLine, summary: "manage standing approval grants and denials", handler: textCmd((*model).grantsCommand)},
		{name: "help", usage: "/help", summary: "show this help", handler: runHelpCommand},
		{name: "jobs", usage: "/jobs [run <name>]", summary: "list or run scheduled jobs", handler: runJobsCommand, plain: runPlainBackend, needsBackend: true},
		{name: "logs", usage: "/logs", summary: "open runtime logs", handler: runLogsCommand},
		{name: "memory", usage: "/memory", summary: "show memory status and recent records", handler: runBackendCommand, plain: runPlainBackend, needsBackend: true},
		{name: "notify", usage: "/notify [off|bell|desktop]", summary: "attention signals: bell or desktop notification when input is needed", handler: voidCmd((*model).setNotifyMode)},
		{name: "mouse", usage: "/mouse [on|off|all]", summary: "mouse tracking: off hands selection back to the terminal", handler: voidCmd((*model).setMouseMode)},
		{name: "model", usage: "/model [name]", summary: "list or switch models", handler: runModelCommand, plain: sessionText(plainModelText)},
		{name: "reasoning", usage: "/reasoning [none|minimal|low|medium|high|xhigh|max|default]", summary: "show or set llama.cpp reasoning effort", handler: sessionCmd(reasoningFeedback), plain: sessionText(reasoningFeedback)},
		{name: "profile", usage: "/profile [name]", summary: "list profiles or switch model+prompt", handler: sessionCmd(profileCommandText), plain: sessionText(profileCommandText)},
		{name: "progress", usage: "/progress [all|new|off]", summary: "show or set tool progress", handler: voidCmd((*model).setProgress)},
		{name: "recorded", usage: "/recorded", summary: "show recorded calls for this request (alt+r)", handler: func(m *model, _ string, _ []string) tea.Cmd { m.showRecorded(); return nil }, plain: func(context.Context, CommandBackend, turn.Session, string, []string) (string, bool) {
			return "Recorded-call overlay unavailable in plain mode; see printed execution history", true
		}},
		{name: "settings", usage: "/settings", summary: "open the settings panel (ctrl+o)", handler: runSettingsCommand, plain: sessionText(settingsPlainText)},
		{name: "retry", usage: "/retry", summary: "retry the last prompt", handler: runRetryCommand},
		{name: "skills", usage: "/skills [install [<name>]]", summary: "list skills, or install from the skill hubs", handler: runSkillsCommand, plain: runPlainBackend, needsBackend: true},
		{name: "syntax", usage: "/syntax [name|auto]", summary: "pick the syntax theme for code blocks", handler: runSyntaxCommand},
		{name: "tools", usage: "/tools", summary: "list and toggle available tools", handler: runToolsCommand, plain: sessionText(plainToolsText)},
	}
	findCommand("help").plain = sessionText(func(turn.Session, []string) string { return plainCommandHelp() })
}

type commandResultMsg struct {
	text string
	err  error
}

func parseCommand(text string) (string, []string, bool) {
	if !strings.HasPrefix(text, "/") {
		return "", nil, false
	}
	fields := strings.Fields(strings.TrimPrefix(text, "/"))
	if len(fields) == 0 {
		return "", nil, true
	}
	return strings.ToLower(fields[0]), fields[1:], true
}

func commandSuggestions() []string {
	return toolcore.MapSlice(commandRegistry, func(command commandSpec) string { return "/" + command.name })
}

func renderCommandHelp(width int) string {
	labels := toolcore.MapSlice(commandRegistry, func(c commandSpec) string { return renderCommandUsage(c.usage) })
	summaries := toolcore.MapSlice(commandRegistry, func(c commandSpec) string { return c.summary })
	// The overlay's title row is the heading, so the rows stand on their own; a
	// COMMANDS rule under a "help" title reads as two headings in a row.
	return strings.TrimRight(twoColumnRows(labels, summaries, width), "\n")
}

// plainCommandHelp is the plain (non-TUI) /help, which has no overlay title to
// carry the heading the rows sit under, so it keeps the rule the overlay drops.
func plainCommandHelp() string { return sectionRule("COMMANDS", 0) + "\n" + renderCommandHelp(0) }

// renderUsage paints a command's usage string the way /tools paints a tool:
// the name itself in the brand gradient, its arguments in the primary accent.
func renderCommandUsage(usage string) string {
	name, args, _ := strings.Cut(usage, " ")
	out := ui.Invokr.Accent.Render(name)
	if args != "" {
		out += " " + ui.Invokr.Info.Render(args)
	}
	return out
}

// twoColumnRows lays label/description pairs out as two columns: labels are
// styled by the caller, descriptions wrap inside whatever width is left and are
// muted. Rows are joined on their top edge, so a description spanning several
// lines pushes nothing sideways.
func twoColumnRows(labels, descriptions []string, width int) string {
	if width <= 0 {
		width = defaultOverlayWidth
	}
	labelWidth := 0
	for _, label := range labels {
		labelWidth = max(labelWidth, ansi.StringWidth(label))
	}
	descWidth := max(minDescriptionWidth, width-labelWidth-2)
	var b strings.Builder
	for i, label := range labels {
		desc := ui.Invokr.Muted.Render(wrapText(descriptions[i], descWidth))
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, tuikit.Pad(label, labelWidth+2), desc) + "\n")
	}
	return b.String()
}

// defaultOverlayWidth is the fallback for the plain (non-TUI) renderers, which
// have no terminal size to lay out against. minDescriptionWidth keeps the
// second column readable when the label column is unusually wide.
const (
	defaultOverlayWidth = 100
	minDescriptionWidth = 20
)

func findCommand(name string) *commandSpec {
	if i := slices.IndexFunc(commandRegistry, func(spec commandSpec) bool { return spec.name == name }); i >= 0 {
		return &commandRegistry[i]
	}
	return nil
}

func (m *model) runCommand(text string) (tea.Cmd, bool) {
	spec, args, feedback, slash := m.commandRequest(text)
	if !slash {
		return nil, false
	}
	m.input.Reset()
	if feedback != "" {
		m.commandMessage(feedback)
		return nil, true
	}
	cmd := spec.handler(m, text, args)
	m.syncChatViewport(true)
	return cmd, true
}

func (m *model) commandRequest(text string) (*commandSpec, []string, string, bool) {
	name, args, slash := parseCommand(text)
	if !slash {
		return nil, nil, "", false
	}
	spec := findCommand(name)
	if spec == nil {
		return nil, args, "unknown command, try /help", true
	}
	if spec.needsBackend && m.commands == nil {
		return nil, args, "command unavailable in this runtime", true
	}
	return spec, args, "", true
}

func (m *model) commandMessage(text string) { m.chatLog.Append(message{role: "tool", text: text}) }

// commandOverlay shows reference output — a help table, a usage report, a tool
// list — in the floating popup instead of the transcript, so it never mixes
// with the conversation and esc puts it away.
// overlayContentWidth is the widest a laid-out overlay body can be before the
// popup re-wraps it. tuikit's overlay grows its box to the content, clamped to
// the frame less a one-cell margin on each side, then wraps at the box width
// less its 4 cells of horizontal chrome — anything wider than that loses the
// column layout as the wrapped tail restarts at column zero.
func (m *model) overlayContentWidth() int { return max(minDescriptionWidth, m.width-6) }

func (m *model) setProgress(args []string) {
	if len(args) > 0 {
		mode, ok := parseMode[progressMode](progressModeNames, args[0])
		if len(args) != 1 || !ok || args[0] == "" {
			m.commandMessage("usage: /progress [all|new|off]")
			return
		}
		m.chatLog.toolProgress = mode
	}
	m.commandMessage("tool progress: " + m.chatLog.toolProgress.String() + " (session only)")
}

// progressModeNames is the tool-progress modes in value order, as /progress
// and INVOKR_TOOL_PROGRESS spell them.
var progressModeNames = []string{"all", "new", "off"}

func (p progressMode) String() string { return progressModeNames[p] }
func (m *model) retryLast() tea.Cmd {
	if m.sending {
		m.commandMessage("cannot retry while a turn is running")
		return nil
	}
	// lastPrompt, not m.history: history is seeded from the persisted
	// cross-session prompt DB, so it would replay a prompt from a past session.
	if m.lastPrompt == "" {
		m.commandMessage("no prompt sent in this session to retry")
		return nil
	}
	m.input.SetValue(m.lastPrompt)
	_, cmd := m.submit()
	return cmd
}

// approvalModes is the cycle shift+tab walks, and the set /approvals accepts.
var approvalModes = []approval.Mode{approval.Manual, approval.Auto, approval.Plan}

// cycleApprovalMode is the shift+tab shortcut: the same switch /approvals makes,
// without leaving the keyboard.
func (m *model) cycleApprovalMode() {
	next := (slices.Index(approvalModes, m.policy.Mode()) + 1) % len(approvalModes)
	m.setApprovalMode([]string{string(approvalModes[next])})
}

func (m *model) setApprovalMode(args []string) {
	if len(args) == 0 {
		m.commandMessage("mode: " + string(m.policy.Mode()))
		return
	}
	if len(args) != 1 {
		m.commandMessage("usage: /mode [manual|auto|plan]")
		return
	}
	mode := args[0]
	if mode == "default" {
		mode = "manual"
	}
	if err := m.policy.SetMode(approval.Mode(mode)); err != nil {
		m.commandMessage(err.Error())
		return
	}
	m.commandMessage("mode: " + mode + " (future calls; running work keeps its permissions)")
}

func (m *model) setPublishing(args []string) {
	if len(args) == 1 && slices.Contains([]string{"ask", "allow"}, args[0]) {
		m.policy.SetPublishing(args[0] == "allow")
	} else if len(args) != 0 {
		m.commandMessage("usage: /publishing [ask|allow]")
		return
	}
	state := "ask"
	if m.policy.Publishing() {
		state = "allow"
	}
	m.commandMessage("Auto publishing: " + state + " (this session only)")
}

func (m *model) setReads(args []string) {
	if len(args) == 1 && slices.Contains([]string{config.ReadsWorkspace, config.ReadsAsk}, args[0]) {
		m.policy.SetOutsideReads(args[0] == config.ReadsAsk)
	} else if len(args) != 0 {
		m.commandMessage("usage: /reads [workspace|ask]")
		return
	}
	m.commandMessage("reads outside the workspace: " + readsState(m.policy) + " (this session only)")
}

func readsState(p *approval.Policy) string {
	return map[bool]string{false: config.ReadsWorkspace, true: config.ReadsAsk}[p.OutsideReads()]
}

func (m *model) modeBanner() string {
	if m.policy.Mode() == approval.YOLO {
		return ui.Invokr.Warn.Width(max(1, m.width)).Render(approval.YOLOWarning)
	}
	if n := m.policy.PriorWork(); n > 0 {
		return ui.Invokr.Warn.Render(fmt.Sprintf("%d active calls/processes retain permissions from a previous mode", n))
	}
	return ""
}

func plainCommand(ctx context.Context, backend CommandBackend, session turn.Session, text string) (string, bool) {
	name, args, slash := parseCommand(text)
	if !slash {
		return "", false
	}
	spec := findCommand(name)
	if spec == nil {
		return "unknown command, try /help", true
	}
	if spec.plain == nil {
		return "command is available only in interactive mode", true
	}
	return spec.plain(ctx, backend, session, name, args)
}

func plainModelText(session turn.Session, args []string) string {
	if len(args) > 1 {
		return "usage: /model <name>"
	}
	if len(args) == 1 {
		switchModelOnSession(session, args[0])
		return "model set to " + args[0] + " (applies from next turn)"
	}
	var out strings.Builder
	listModelsPlain(&out, session)
	return out.String()
}

// compactCommandText requests compaction and reports what it will reclaim; both
// /compact paths do exactly this.
func compactCommandText(session turn.Session, _ []string) string {
	requestContextCompaction(session)
	return compactFeedback(session)
}

func runPlainBackend(ctx context.Context, backend CommandBackend, _ turn.Session, name string, args []string) (string, bool) {
	if backend == nil {
		return "command unavailable in this runtime", true
	}
	result, err := backend.Execute(ctx, name, args)
	if err != nil {
		return "error: " + err.Error(), true
	}
	return result, true
}

func plainContextText(session turn.Session, _ []string) string {
	usage, ok := sessionUsage(session)
	if !ok || !usage.Available {
		return "usage unavailable"
	}
	return renderContextUsage(usage, contextBreakdown(session), compactionEstimate(session), compactionThreshold(session, usage.ContextWindowTokens))
}

func plainToolsText(session turn.Session, _ []string) string {
	source, ok := session.(toolSpecSource)
	if !ok {
		return "tools unavailable in this runtime"
	}
	return renderTools(source.ToolSpecs(), 0)
}

func runHelpCommand(m *model, _ string, _ []string) tea.Cmd {
	m.overlay.Open("help", renderCommandHelp(m.overlayContentWidth()))
	return nil
}

func runLogsCommand(m *model, _ string, _ []string) tea.Cmd {
	if !m.logsAvailable() {
		m.commandMessage("log panel unavailable (no log source; start with --log-view)")
		return nil
	}
	m.showLogs()
	return nil
}

func runClearCommand(m *model, _ string, _ []string) tea.Cmd {
	if m.approvalQueue != nil && m.approvalQueue.Len() > 0 {
		m.commandMessage("cannot clear while approvals are pending")
		return nil
	}
	if m.sending {
		m.commandMessage("cannot start a new session while a turn is running")
		return nil
	}
	m.err = ErrNewSession
	return tea.Quit
}

func runContextCommand(m *model, _ string, _ []string) tea.Cmd {
	usage, ok := sessionUsage(m.session)
	if !ok || !usage.Available {
		m.commandMessage("usage unavailable")
	} else {
		m.overlay.Open("context", renderContextUsage(usage, contextBreakdown(m.session), compactionEstimate(m.session), compactionThreshold(m.session, usage.ContextWindowTokens)))
	}
	return nil
}

func runRetryCommand(m *model, _ string, _ []string) tea.Cmd { return m.retryLast() }

func runJobsCommand(m *model, text string, args []string) tea.Cmd {
	if len(args) > 0 && args[0] == "run" && m.sending {
		m.commandMessage("wait for the current turn to finish")
		return nil
	}
	return runBackendCommand(m, text, args)
}

func runBackendCommand(m *model, text string, args []string) tea.Cmd {
	name, _, _ := parseCommand(text)
	return func() tea.Msg {
		result, err := m.commands.Execute(m.ctx, name, args)
		return commandResultMsg{text: result, err: err}
	}
}

type toolSpecSource interface{ ToolSpecs() []toolcore.ToolSpec }
type usageSource interface{ Usage() turn.TokenUsage }

func runToolsCommand(m *model, _ string, _ []string) tea.Cmd {
	source, ok := m.session.(toolSpecSource)
	if !ok {
		m.commandMessage("tools unavailable in this runtime")
		return nil
	}
	specs := source.ToolSpecs()
	manager, ok := m.session.(toolDialogSource)
	if !ok {
		m.overlay.Open("tools", renderTools(specs, m.overlayContentWidth()))
		return nil
	}
	listings, err := manager.ListTools(m.ctx)
	if err != nil {
		m.overlay.Open("tools", "error: "+err.Error())
		return nil
	}
	m.openToolDialog(specs, listings)
	return nil
}

func sessionUsage(session turn.Session) (turn.TokenUsage, bool) {
	if source, ok := session.(usageSource); ok {
		return source.Usage(), true
	}
	return turn.TokenUsage{}, false
}

// compactFeedback stays quiet on an empty transcript: compaction is still
// requested, but "context before compaction: 0 / N" is noise, not feedback.
func compactFeedback(session turn.Session) string {
	usage, ok := sessionUsage(session)
	if !ok || usage.LatestPromptTokens+usage.LatestCompletionTokens == 0 {
		return "nothing to compact yet"
	}
	return "context compaction requested\ncontext before compaction: " + contextUsageLine(usage)
}

func renderContextUsage(usage turn.TokenUsage, breakdown turn.ContextBreakdown, reclaimable int64, compactPercent int) string {
	// The overlay's title row is the heading, so the body opens at the bar; a
	// CONTEXT rule under a "context" title reads as the same line twice. The
	// plain renderer has no title, and its first line already reads
	// "context: N / M tokens", which names the section.
	var body string
	if bar := renderContextSegments(usage, breakdown); bar != "" {
		body = bar + "\n"
	}
	return body + renderContextTotals(usage, compactPercent) + lastTurnLine(usage) + costLine(usage) + compactHint(reclaimable)
}

func compactionThreshold(session turn.Session, window int64) int {
	if source, ok := session.(interface{ CompactionThreshold() int }); ok {
		return source.CompactionThreshold()
	}
	return constants.ContextCompactThreshold(window)
}

// contextBreakdownSource is the optional session capability behind the
// per-category /context bar; anything but the real agent.Session falls back to
// the prompt/completion/free split.
type contextBreakdownSource interface{ ContextBreakdown() turn.ContextBreakdown }

func contextBreakdown(session turn.Session) turn.ContextBreakdown {
	if source, ok := session.(contextBreakdownSource); ok {
		return source.ContextBreakdown()
	}
	return turn.ContextBreakdown{}
}

// lastTurnLine reports completion-side and cache counters that do not occupy
// the context window, so they stay off the bar (reasoning is not carried into
// later prompts; cached tokens are a subset of the prompt).
func lastTurnLine(usage turn.TokenUsage) string {
	var parts []string
	if usage.LatestReasoningTokens > 0 {
		parts = append(parts, "reasoning "+formatTokensCompact(usage.LatestReasoningTokens))
	}
	if usage.LatestCachedPromptTokens > 0 && usage.LatestPromptTokens > 0 {
		parts = append(parts, fmt.Sprintf("cached %s (%.0f%%)", formatTokensCompact(usage.LatestCachedPromptTokens), float64(usage.LatestCachedPromptTokens)/float64(usage.LatestPromptTokens)*100))
	}
	if len(parts) == 0 {
		return ""
	}
	return ui.Invokr.Muted.Render("\nlast turn: " + strings.Join(parts, " · "))
}

// compactionEstimator is the optional session capability behind the design's
// "/compact reclaims an estimated 9.4k" footer. A session that cannot say —
// anything but the real agent.Session — simply omits the line.
type compactionEstimator interface{ CompactionEstimate() int64 }

func compactionEstimate(session turn.Session) int64 {
	if estimator, ok := session.(compactionEstimator); ok {
		return estimator.CompactionEstimate()
	}
	return 0
}

// compactHint is the design's /context footer. It stays silent rather than
// advertising a reclaim of zero when there is nothing old enough to summarize.
func compactHint(reclaimable int64) string {
	if reclaimable <= 0 {
		return ""
	}
	return ui.Invokr.Muted.Render("\n/compact reclaims an estimated " + formatTokensCompact(reclaimable))
}

// renderContextSegments draws the window as a filled bar split by where the
// tokens went, with a swatch legend under it. With a session that can attribute
// usage (agent.Session), the bar is split per category — prompt sections, tool
// schemas, message kinds, plus an "unattributed" residual so it sums to the
// provider-reported total; otherwise it falls back to prompt/completion/free,
// restated as a two-category breakdown so both paths share one renderer.
func renderContextSegments(usage turn.TokenUsage, breakdown turn.ContextBreakdown) string {
	if !usage.Available || usage.ContextWindowTokens <= 0 {
		return ""
	}
	if !breakdown.Available || breakdown.WindowTokens <= 0 || len(breakdown.Categories) == 0 {
		breakdown = turn.ContextBreakdown{
			WindowTokens: usage.ContextWindowTokens,
			FreeTokens:   max(0, usage.ContextWindowTokens-usage.LatestPromptTokens-usage.LatestCompletionTokens),
			Categories: []turn.ContextCategory{
				{Name: "prompt", Tokens: usage.LatestPromptTokens}, {Name: "completion", Tokens: usage.LatestCompletionTokens},
			},
		}
	}
	return renderBreakdownSegments(breakdown)
}

// contextSegmentStyles colors breakdown segments and legend swatches, cycled
// when there are more categories than styles.
var contextSegmentStyles = []*lipgloss.Style{&ui.Invokr.Accent, &ui.Invokr.Info, &ui.Invokr.Warn, &ui.Invokr.Success}

// renderBreakdownSegments maps the categories onto tuikit's SegmentBar.
func renderBreakdownSegments(breakdown turn.ContextBreakdown) string {
	window := float64(breakdown.WindowTokens)
	segments := make([]tuikit.Segment, len(breakdown.Categories))
	for index, category := range breakdown.Categories {
		segments[index] = tuikit.Segment{Label: category.Name, Value: humanize.Comma(category.Tokens),
			Share: float64(category.Tokens) / window, Style: *contextSegmentStyles[index%len(contextSegmentStyles)]}
	}
	bar := tuikit.SegmentBar(maxContextBarWidth, segments, tuikit.Segment{Label: "free",
		Value: humanize.Comma(breakdown.FreeTokens), Share: float64(breakdown.FreeTokens) / window, Style: ui.Invokr.Muted})
	if breakdown.Estimated {
		bar = ui.Invokr.Muted.Render("(estimated — no provider usage reported yet)") + "\n" + bar
	}
	return bar
}

// costLine is empty unless provider.pricing is configured — no pricing means
// "cost unknown", never "cost zero".
func costLine(usage turn.TokenUsage) string {
	if usage.SessionCost <= 0 {
		return ""
	}
	return fmt.Sprintf("\nsession cost: %.4f", usage.SessionCost)
}

func renderContextTotals(usage turn.TokenUsage, compactPercent int) string {
	trigger := "window size unknown"
	if usage.ContextWindowTokens > 0 {
		trigger = fmt.Sprintf("compaction triggers at %d%% (%s tokens); warning at %d%%", compactPercent,
			humanize.Comma(usage.ContextWindowTokens*int64(compactPercent)/100), constants.ContextWarningPercent)
	}
	return contextUsageLine(usage) + "\n" + trigger + "\n" + sessionTotalsLine(usage)
}

// sessionTotalsLine splits the session prompt total into the cache-served and
// freshly-evaluated halves whenever the provider reported cached tokens, so
// /context shows what the prompt cache actually saved.
func sessionTotalsLine(usage turn.TokenUsage) string {
	prompt := humanize.Comma(usage.SessionPromptTokens) + " prompt"
	if cached := usage.SessionCachedPromptTokens; cached > 0 {
		prompt += fmt.Sprintf(" (%s cached · %s uncached)", humanize.Comma(cached), humanize.Comma(usage.SessionPromptTokens-cached))
	}
	return "session totals: " + prompt + " · " + humanize.Comma(usage.SessionCompletionTokens) + " completion"
}

func contextUsageLine(usage turn.TokenUsage) string {
	used := usage.LatestPromptTokens + usage.LatestCompletionTokens
	if usage.ContextWindowTokens == 0 {
		return fmt.Sprintf("context: %s / ? tokens", humanize.Comma(used))
	}
	return fmt.Sprintf("context: %s / %s tokens (%.0f%%)", humanize.Comma(used), humanize.Comma(usage.ContextWindowTokens), float64(used)/float64(usage.ContextWindowTokens)*100)
}

func renderTools(specs []toolcore.ToolSpec, width int) string {
	if len(specs) == 0 {
		return "no tools available"
	}
	groups := map[string][]toolcore.ToolSpec{}
	for _, spec := range specs {
		source := cmp.Or(strings.TrimSpace(spec.Metadata.Source), "native")
		groups[source] = append(groups[source], spec)
	}
	// Lexicographic, except that the native tools always head the listing.
	keys := slices.Sorted(maps.Keys(groups))
	if i := slices.Index(keys, "native"); i > 0 {
		keys = slices.Insert(slices.Delete(keys, i, i+1), 0, "native")
	}
	var out strings.Builder
	for _, source := range keys {
		header := "toolbox (" + source + ")"
		if source == "native" {
			header = "native tools"
		}
		slices.SortFunc(groups[source], func(a, b toolcore.ToolSpec) int { return cmp.Compare(a.Name, b.Name) })
		labels := toolcore.MapSlice(groups[source], func(spec toolcore.ToolSpec) string { return ui.Invokr.Accent.Render(toolDisplayName(spec)) })
		descriptions := toolcore.MapSlice(groups[source], func(spec toolcore.ToolSpec) string { return shortDescription(spec.Description) })
		fmt.Fprintf(&out, "%s\n%s", sectionRule(strings.ToUpper(header), 0), twoColumnRows(labels, descriptions, width))
	}
	return strings.TrimSpace(out.String())
}

func toolDisplayName(spec toolcore.ToolSpec) string {
	if !spec.Metadata.AlwaysVisible && spec.Parameters == nil {
		return spec.Name + " (deferred)"
	}
	return spec.Name
}

func shortDescription(value string) string {
	// The model-facing first sentence, punctuated for display.
	sentence := tool_search.ShortDescription(value)
	if sentence != "" && !strings.HasSuffix(sentence, ".") {
		sentence += "."
	}
	return sentence
}

// runSyntaxCommand opens the stylesheet picker, or applies a name given
// outright. An unknown name is reported rather than silently ignored: tuikit
// would correct it to its default, which looks like the command did nothing.
func runSyntaxCommand(m *model, _ string, args []string) tea.Cmd {
	if len(args) == 0 {
		m.syntaxPicker.showSyntax(syntaxTheme)
		return nil
	}
	name := args[0]
	switch {
	case strings.EqualFold(name, autoSyntaxTheme):
		m.applySyntaxTheme("")
	case !slices.Contains(markdown.SyntaxThemes(), name):
		m.commandMessage("unknown syntax theme " + name + " — /syntax lists them")
	default:
		m.applySyntaxTheme(name)
	}
	return nil
}
