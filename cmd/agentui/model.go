package agentui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/surface/ui"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/antonikliment/tuikit"
	"github.com/charmbracelet/x/ansi"
)

type model struct {
	ctx           context.Context
	session       turn.Session
	historyDB     turn.PromptHistory
	logPanel      logPanel
	logView       bool
	toolEvents    *toolEventQueue
	approvalQueue *approval.ApprovalQueue
	commands      CommandBackend
	policy        *approval.Policy
	grants        *approval.Store
	approvals     approvalPanel
	chatLog       chatLog
	statusline    statusline
	selection     tuikit.Selection
	mouseMode     mouseMode
	// copiedChars sizes the copy notice, copiedAt times it out. Both are only
	// read while the selection that produced them is still on screen.
	copiedChars int
	copiedAt    time.Time
	// frame is the last composed view, kept so a selection can be copied out
	// of exactly what was on screen when the drag ended.
	frame      string
	completion completion
	// The four list overlays that take the input area's place; at most one is
	// open at a time (see inputArea).
	modelPicker, syntaxPicker, skillPicker, palette listPicker
	// overlay is the modal popup drawn over the finished frame.
	overlay    tuikit.Overlay
	settings   settingsPanel
	chat       viewport.Model
	help       help.Model
	input      textarea.Model
	spinner    spinner.Model
	keys       keyMap
	history    []string
	historyPos int
	// lastPrompt is what /retry replays; draft is the half-typed line the
	// history walk displaced, and browsing says the walk is in progress.
	lastPrompt, draft               string
	browsing, sending, userScrolled bool
	// cancelSend stops the turn in flight; stopping marks that the cancel was
	// deliberate, so the response path renders a stop notice, not an error.
	cancelSend  context.CancelFunc
	sentAt      time.Time
	stopping    bool
	quitArmed   time.Time
	usage       turn.TokenUsage
	hasUsage    bool
	notify      notifier
	lastPending int
	// focusMode is the transcript-focus (fold) mode; focusMsg is the message
	// index its cursor sits on; blockRanges are the [start,end) content-line
	// ranges of foldable blocks in the last composed chat content.
	focusMode     bool
	focusMsg      int
	blockRanges   []blockRange
	hideHints     bool
	prefsPath     string
	width, height int
	err           error
}

// runningSpinner is the design's running-state glyph (panels 07 and 08 both
// draw "◐"), turned into a four-frame rotation. spinner.Line's "|/-\" reads as
// ASCII filler next to the ✓/✕ outcome glyphs.
var runningSpinner = spinner.Spinner{
	Frames: []string{"◐", "◓", "◑", "◒"},
	FPS:    time.Second / 8,
}

type progressMode int

const (
	progressAll progressMode = iota // show every tool block (default)
	progressNew                     // collapse repeats of the same tool
	progressOff                     // show running spinners only, drop finished
)

// ponytail: env knob instead of full config/flag plumbing. Promote to a CLI flag
// if it needs to be discoverable.
func progressModeFromEnv() progressMode {
	mode, _ := parseMode[progressMode](progressModeNames, strings.ToLower(strings.TrimSpace(os.Getenv("INVOKR_TOOL_PROGRESS"))))
	return mode
}

type responseMsg struct {
	evidence []turn.ExecutionEvidence
	text     string
	usage    turn.TokenUsage
	failures []turn.ToolOutcome
	err      error
}

type historySavedMsg struct {
	err error
}

type toolEventsReadyMsg struct{}

func newModelWithCommands(ctx context.Context, session turn.Session, commands CommandBackend, toolEvents *toolEventQueue, approvalQueue *approval.ApprovalQueue, logs LogSource, logView bool, promptHistory turn.PromptHistory, initialHistory []string) *model {
	keys := defaultKeyMap(logView && logs != nil)
	input := textarea.New()
	input.Placeholder = "ask anything"
	input.CharLimit = 0
	input.ShowLineNumbers = false
	input.DynamicHeight = true
	input.MinHeight = 1
	input.MaxHeight = 6
	input.MaxContentHeight = 100
	input.KeyMap.InsertNewline = keys.Newline
	input.SetWidth(96)
	input.Focus()

	chat := viewport.New(viewport.WithWidth(96), viewport.WithHeight(23))
	for _, binding := range []*key.Binding{&chat.KeyMap.Up, &chat.KeyMap.Down, &chat.KeyMap.HalfPageUp, &chat.KeyMap.HalfPageDown, &chat.KeyMap.Left, &chat.KeyMap.Right} {
		binding.Unbind()
	}

	m := &model{
		ctx:           ctx,
		session:       session,
		historyDB:     promptHistory,
		logPanel:      newLogPanel(logs),
		logView:       logView,
		toolEvents:    toolEvents,
		approvalQueue: approvalQueue,
		commands:      commands,
		policy:        approval.NewPolicy(approval.Manual, false),
		chatLog:       newChatLog(progressModeFromEnv()),
		statusline:    newStatusline(),
		overlay:       tuikit.NewOverlay(ui.Invokr.Palette),
		chat:          chat,
		help:          help.New(),
		input:         input,
		spinner:       spinner.New(spinner.WithSpinner(runningSpinner)),
		keys:          keys,
		history:       append([]string(nil), initialHistory...),
		historyPos:    -1,
		width:         96,
		height:        24,
	}
	if approvalQueue != nil && approvalQueue.Policy != nil {
		m.policy = approvalQueue.Policy
	}
	m.applyTheme()
	return m
}

// applyTheme pushes the current palette into the bubbles components, which cache
// styles by value. Called at construction and again whenever the terminal
// reports its background color.
func (m *model) applyTheme() {
	// Only the cursor's row carries the caret; the other rows of a multi-line
	// draft are blank, the way a shell's continuation prompt reads.
	m.input.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber != cursorDisplayRow(m.input) {
			return ""
		}
		return renderPrompt()
	})
	inputStyles := m.input.Styles()
	inputStyles.Focused.Prompt, inputStyles.Blurred.Prompt = ui.Invokr.Frame, ui.Invokr.Frame
	inputStyles.Focused.Text, inputStyles.Blurred.Text = ui.Invokr.Text, ui.Invokr.Text
	inputStyles.Focused.Placeholder, inputStyles.Blurred.Placeholder = ui.Invokr.Muted, ui.Invokr.Muted
	inputStyles.Cursor.Color = ui.Invokr.Palette.Brand
	// bubbles paints the cursor row with a filled background by default, which
	// reads as a gray bar across the whole width. The box drawn in inputArea is
	// what marks the input instead.
	inputStyles.Focused.CursorLine, inputStyles.Blurred.CursorLine = lipgloss.NewStyle(), lipgloss.NewStyle()
	m.input.SetStyles(inputStyles)

	m.help.Styles.ShortKey, m.help.Styles.FullKey = ui.Invokr.Accent, ui.Invokr.Accent
	m.help.Styles.ShortDesc, m.help.Styles.FullDesc = ui.Invokr.Muted, ui.Invokr.Muted
	m.help.Styles.ShortSeparator, m.help.Styles.FullSeparator = ui.Invokr.Frame, ui.Invokr.Frame

	m.spinner.Style = ui.Invokr.Info
	m.overlay.Theme = ui.Invokr.Palette

	// The markdown renderer captures the palette when it is built, and the
	// streamer caches what it has already drawn, so both have to be replaced
	// rather than told about the change. The per-message render memo holds the
	// old colors too, so it drops with them.
	resetMarkdown()
	m.chatLog.InvalidateRender()
}

// savePrefs writes every preference the TUI remembers, so saving one does not
// drop another.
func (m *model) savePrefs() {
	savePrefs(m.prefsPath, prefs{
		HideHints:   m.hideHints,
		SyntaxTheme: syntaxTheme,
		MouseMode:   m.mouseMode.String(),
		NotifyMode:  m.notify.mode.String(),
	})
}

func (m *model) Init() tea.Cmd { return tea.Batch(textarea.Blink, tea.RequestBackgroundColor) }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// huh's own messages (field focus, blink) come back through here, so an
	// open settings form sees every non-key message as well as the model.
	if _, isKey := msg.(tea.KeyPressMsg); m.settings.open && !isKey {
		cmd := m.updateSettings(msg)
		updated, next := m.update(msg)
		return updated, tea.Batch(cmd, next)
	}
	return m.update(msg)
}

func (m *model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.updateWindowSize(msg)
	case tea.BackgroundColorMsg:
		return m.updateBackgroundColor(msg)
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	case tea.MouseWheelMsg, tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg, copyNoticeExpiredMsg:
		return m.updatePointer(msg)
	case responseMsg:
		m.selection.Clear()
		return m.updateResponse(msg)
	case historySavedMsg:
		m.err = cmp.Or(msg.err, m.err)
		return m, nil
	case spinner.TickMsg:
		return m.updateSpinner(msg)
	case toolEventsReadyMsg:
		m.selection.Clear()
		return m.updateToolEvents()
	case modelListMsg:
		return m.handleModelListMsg(msg)
	case commandResultMsg:
		return m.updateCommandResult(msg)
	case skillPromptMsg, hubSkillsMsg:
		return m.updateSkillMsg(msg)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// updateBackgroundColor rebuilds the palette for the terminal's actual
// background, so the UI reads on light and dark themes alike.
func (m *model) updateBackgroundColor(msg tea.BackgroundColorMsg) (tea.Model, tea.Cmd) {
	ui.Invokr = ui.NewTheme(msg.IsDark())
	m.applyTheme()
	return m, nil
}

func (m *model) updateCommandResult(msg commandResultMsg) (tea.Model, tea.Cmd) {
	text := msg.text
	if msg.err != nil {
		text = "error: " + msg.err.Error()
	}
	m.commandMessage(text)
	m.syncChatViewport(true)
	return m, nil
}

func (m *model) updateWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width, m.height = msg.Width, msg.Height
	m.input.SetWidth(max(20, m.mainWidth()-4))
	m.help.SetWidth(max(20, m.mainWidth()))
	m.syncChatViewport(true)
	m.syncLogViewport()
	return m, nil
}

// updatePointer routes the mouse and the copy-notice tick. The wheel scrolls,
// a left drag selects, and both clear the highlight first: it is anchored to
// screen cells, not to the text it covered, so anything that moves the text
// takes it down. The tick only exists to repaint a stale notice away.
func (m *model) updatePointer(msg tea.Msg) (tea.Model, tea.Cmd) {
	if wheel, ok := msg.(tea.MouseWheelMsg); ok {
		m.selection.Clear()
		return m.updateMouseWheel(wheel)
	}
	cmd, _ := m.updateMouse(msg)
	return m, cmd
}

// updateControlKey handles the keys that outrank the prompt: the readline pair
// (see updateReadlineKey) and the two things esc means before the prompt sees it:
// dropping a highlight, and interrupting a turn in flight. The highlight wins
// when both apply — losing a turn to a keystroke the user meant as "deselect"
// is the worse surprise, and a second esc still interrupts.
func (m *model) updateControlKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if updated, cmd, handled := m.updateReadlineKey(msg); handled {
		return updated, cmd, true
	}
	if !key.Matches(msg, m.keys.Interrupt) {
		return nil, nil, false
	}
	if !m.selection.Empty() {
		m.selection.Clear()
		return m, nil, true
	}
	if m.sending {
		updated, cmd := m.handleInterrupt()
		return updated, cmd, true
	}
	return nil, nil, false
}

// updateReadlineKey handles the shell staples: ctrl+l redraws a garbled screen,
// and ctrl+d on an empty line is end-of-input — it quits through the same
// two-press path as ctrl+c. With text in the input ctrl+d stays the textarea's
// delete-forward.
func (m *model) updateReadlineKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.Redraw):
		return m, tea.ClearScreen, true
	case key.Matches(msg, m.keys.CycleMode):
		m.cycleApprovalMode()
		return m, nil, true
	case key.Matches(msg, m.keys.EOF) && m.input.Value() == "":
		updated, cmd := m.handleInterrupt()
		return updated, cmd, true
	}
	return nil, nil, false
}

func (m *model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Quit) {
		m.selection.Clear()
		return m.handleInterrupt()
	}

	if cmd, handled := m.updateModalKey(msg); handled {
		return m, cmd
	}
	if m.updateLogsModeKey(msg) {
		return m, nil
	}
	if updated, cmd, handled := m.updatePendingQueueKey(msg); handled {
		return updated, cmd
	}
	if handled, cmd := m.updateFoldOrPaletteKey(msg); handled {
		return m, cmd
	}
	if cmd, handled := m.updateOverlayKey(msg); handled {
		return m, cmd
	}
	if updated, cmd, handled := m.updateControlKey(msg); handled {
		return updated, cmd
	}
	// Typing ends a selection, the way it does in an editor.
	m.selection.Clear()
	switch {
	case key.Matches(msg, m.keys.Submit):
		return m.submit()
	case key.Matches(msg, m.keys.Help):
		return m.showHelp()
	case m.updateHistoryKey(msg):
	default:
		m.stopBrowsing()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.completion.refresh(m.input.Value())
	return m, cmd
}

// updateModalKey routes keys to the popup and the settings panel. Both are
// modal: while one is showing it owns every key but ctrl+c, including the esc
// that would otherwise deny approvals or interrupt a turn.
func (m *model) updateModalKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if m.overlay.Update(msg) {
		if !m.overlay.IsOpen() {
			// ctrl+h opens the popup and expands the footer block together, so
			// dismissing the popup has to take the block back down with it.
			m.help.ShowAll = false
			m.syncChatViewport(true)
		}
		return nil, true
	}
	return m.updateSettingsKey(msg)
}

// updateOverlayKey offers the key to whatever is layered over the transcript —
// the model picker, the completion popup, the view switch, the chat scroll —
// and reports whether one of them claimed it. Order is the stacking order: an
// open overlay gets first refusal on keys (esc especially) that would otherwise
// mean something to the main view. The modal popup is handled ahead of this,
// in updateKey.
// The skill picker is the one overlay whose choice starts async work, so this
// hands a tea.Cmd back alongside the consumed flag.
func (m *model) updateOverlayKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if cmd, handled := m.updateSkillPickerKey(msg); handled {
		return cmd, true
	}
	handled := m.updateModelPickerKey(msg) || m.updateSyntaxPickerKey(msg) || m.updateCompletionKey(msg) || m.updateViewSwitchKey(msg) || m.updateChatViewportKey(msg)
	return nil, handled
}

// handleInterrupt implements the two-stage ctrl+c: while a turn is in flight
// (or the approval queue is non-empty) it cancels that work and stays open;
// only a press with nothing pending quits.
func (m *model) handleInterrupt() (tea.Model, tea.Cmd) {
	if m.pendingApprovals() > 0 {
		m.approvalQueue.DecideAll(approval.Decision{Approved: false, Reason: "interrupted by user"})
		m.chatLog.Append(message{role: "tool", text: "approval queue cleared"})
		m.approvals.Reset()
		if m.cancelSend != nil {
			m.stopping = true
			m.cancelSend()
		}
		m.syncChatViewport(true)
		return m, nil
	}
	if !m.sending {
		// Idle: quitting takes two presses, so a stray ctrl+c doesn't kill the
		// session. The first arms the footer hint, a second within the window
		// exits.
		if time.Since(m.quitArmed) < quitConfirmWindow {
			return m, m.quitCmd()
		}
		m.quitArmed = time.Now()
		return m, nil
	}
	if m.cancelSend != nil {
		m.cancelSend()
	}
	// The in-flight Send will return context.Canceled and updateResponse
	// renders the stop notice; flag it so that path isn't shown as an error.
	m.stopping = true
	return m, nil
}

// updatePendingQueueKey routes a key to the approval queue while one is
// pending. The bare decision letters act only on an empty input line: once the
// user has typed anything every printable key belongs to the input, because a
// queue that claimed them turned typing a word containing "a" into an approval.
// The same holds for the line-editing keys (backspace, ctrl+u, ctrl+w, delete,
// left/right) so a typed line stays editable. Enter, esc and up/down/j/k reach
// the queue either way.
func (m *model) updatePendingQueueKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	typing := (msg.Text != "" || isLineEditingKey(msg)) && m.input.Value() != ""
	if m.pendingApprovals() == 0 || typing {
		return m, nil, false
	}
	// Enter would otherwise be swallowed here along with every other command
	// while approvals pend; /clear gets a short refusal instead of silence.
	if key.Matches(msg, m.keys.Submit) && strings.TrimSpace(m.input.Value()) == "/clear" {
		m.chatLog.Append(message{role: "tool", text: "approvals pending: decide them first"})
		m.input.Reset()
		m.syncChatViewport(true)
		return m, nil, true
	}
	return m.updateQueueKey(msg)
}

// isLineEditingKey reports whether msg is one of the non-printable keys the
// textarea uses to edit an existing line, as opposed to a queue decision key.
func isLineEditingKey(msg tea.KeyPressMsg) bool {
	switch msg.Code {
	case tea.KeyBackspace, tea.KeyDelete, tea.KeyLeft, tea.KeyRight:
		return true
	}
	return (msg.Code == 'u' || msg.Code == 'w') && msg.Mod.Contains(tea.ModCtrl)
}

// updateQueueKey handles the approval queue's own keys, reporting whether it
// consumed the keypress. An unconsumed key falls through to the normal input
// handling in updateKey.
func (m *model) updateQueueKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.approvals.expanded && m.updateApprovalDetailKey(msg) {
		m.syncChatViewport(true)
		return m, nil, true
	}
	if m.updateChatViewportKey(msg) {
		return m, nil, true
	}
	switch {
	case key.Matches(msg, m.keys.QueueUp):
		m.approvals.Up()
	case key.Matches(msg, m.keys.QueueDown):
		m.approvals.Down(m.approvalQueue.Len())
	case key.Matches(msg, m.keys.ExpandItem):
		m.approvals.Toggle()
	case key.Matches(msg, m.keys.ApproveItem, m.keys.DenyItem):
		updated, cmd := m.decideSelectedItem(key.Matches(msg, m.keys.ApproveItem))
		return updated, cmd, true
	case key.Matches(msg, m.keys.AlwaysAllow, m.keys.AlwaysDeny):
		updated, cmd := m.alwaysDecideSelected(key.Matches(msg, m.keys.AlwaysDeny))
		return updated, cmd, true
	case key.Matches(msg, m.keys.ApproveAll, m.keys.DenyAll):
		updated, cmd := m.decideAll(key.Matches(msg, m.keys.ApproveAll))
		return updated, cmd, true
	default:
		// Printable text belongs to the input; everything else (tab, arrows,
		// modified keys) stays owned by the queue so a pending approval keeps
		// the view locked to the chat.
		return m, nil, msg.Text == ""
	}
	m.syncChatViewport(true)
	return m, nil, true
}

func (m *model) updateApprovalDetailKey(msg tea.KeyPressMsg) bool {
	items := m.approvalQueue.Snapshot()
	if m.approvals.cursor >= len(items) {
		return false
	}
	total := len(approvalDetailLines(items[m.approvals.cursor].Pending.Request, m.mainWidth()))
	switch {
	case key.Matches(msg, m.keys.PageUp):
		m.approvals.Scroll(-approvalDetailHeight, total)
	case key.Matches(msg, m.keys.PageDn):
		m.approvals.Scroll(approvalDetailHeight, total)
	case key.Matches(msg, m.keys.Home):
		m.approvals.detailOffset = 0
	case key.Matches(msg, m.keys.End):
		m.approvals.ScrollToBottom(total)
	default:
		return false
	}
	return true
}

func (m *model) decideSelectedItem(approved bool) (tea.Model, tea.Cmd) {
	if m.approvalQueue == nil {
		return m, nil
	}
	items := m.approvalQueue.Snapshot()
	if len(items) == 0 || m.approvals.cursor >= len(items) {
		return m, nil
	}
	item := items[m.approvals.cursor]
	decision, text := decisionOf(approved, "approval granted: "+item.Pending.Tool, "approval denied: "+item.Pending.Tool)
	m.approvalQueue.Decide(item.Pending.ID, decision)
	m.approvals.Collapse()
	m.chatLog.Append(message{role: "tool", text: text})
	m.clampQueueCursor()
	m.syncChatViewport(true)
	return m, nil
}

// standingGrantHours is how long "g"/"x" stops the queue asking about a call. A
// day covers a working session without becoming an invisible permanent policy.
const standingGrantHours = 24

// alwaysDecideSelected records a standing grant (deny=false) or standing denial
// (deny=true) for the request under the cursor and then applies that decision,
// so "g"/"x" is one keystroke rather than decide-then-/grants.
//
// The rule matches the request's Subject, which is the only thing standing
// rules match on. A request that carries no Subject can never be covered by
// one, so the key declines rather than acting under a rule that would not
// actually match the next identical call.
func (m *model) alwaysDecideSelected(deny bool) (tea.Model, tea.Cmd) {
	verb := ruleVerb[deny]
	items := m.approvalQueue.Snapshot()
	if m.grants == nil || len(items) == 0 || m.approvals.cursor >= len(items) {
		m.chatLog.Append(message{role: "tool", text: "standing grants are disabled"})
		m.syncChatViewport(true)
		return m, nil
	}
	req := items[m.approvals.cursor].Pending.Request
	pattern, err := m.addStandingRule(req, deny)
	if err != nil {
		m.chatLog.Append(message{role: "error", text: "cannot always-" + verb + " " + req.Tool + ": " + err.Error()})
		m.syncChatViewport(true)
		return m, nil
	}
	m.chatLog.Append(message{role: "tool", text: fmt.Sprintf("always %sing %s %q for %dh (/grants to revoke)", verb, req.Tool, pattern, standingGrantHours)})
	return m.decideSelectedItem(!deny)
}

// addStandingRule stores the rule and returns the pattern it matches on, which
// is derived from the subject rather than being the subject itself: a literal
// pipeline can never be a valid grant pattern, so a grant on `find … | sort`
// covers `find …` and its later arguments instead.
func (m *model) addStandingRule(req approval.Request, deny bool) (string, error) {
	if !deny && req.PolicyChecked && (req.Protected || req.Publishes) {
		return "", errors.New("this operation requires one-time approval; approve with a")
	}
	if strings.TrimSpace(req.Subject) == "" {
		return "", errors.New("this request carries no matchable subject")
	}
	pattern, ok := approval.DerivePattern(req.Subject)
	if !ok {
		return "", errors.New("not grantable: contains a pipeline/redirect — approve once with a")
	}
	_, err := m.grants.Add(approval.Grant{
		Deny:       deny,
		Tool:       req.Tool,
		Tier:       req.Tier,
		ArgPattern: pattern,
		Scope:      approval.ScopeGlobal,
		ExpiresAt:  time.Now().Add(standingGrantHours * time.Hour),
		Reason:     "always-decided from the approval queue",
	})
	return pattern, err
}

func (m *model) decideAll(approved bool) (tea.Model, tea.Cmd) {
	n := m.pendingApprovals()
	if n == 0 {
		return m, nil
	}
	decision, text := decisionOf(approved, fmt.Sprintf("approved all %d pending requests", n), fmt.Sprintf("denied all %d pending requests", n))
	m.approvalQueue.DecideAll(decision)
	m.chatLog.Append(message{role: "tool", text: text})
	m.approvals.Reset()
	m.syncChatViewport(true)
	return m, nil
}

// decisionOf pairs the decision handed to the queue with the transcript line
// that records it, which is all the approve and deny paths differ by.
func decisionOf(approved bool, granted, denied string) (approval.Decision, string) {
	if approved {
		return approval.Decision{Approved: true}, granted
	}
	return approval.Decision{Reason: "denied by user"}, denied
}

func (m *model) clampQueueCursor() {
	if m.approvalQueue == nil {
		return
	}
	m.approvals.Clamp(m.approvalQueue.Len())
}

// pendingApprovals is how many requests the queue is showing, zero when there
// is no queue at all.
func (m *model) pendingApprovals() int {
	if m.approvalQueue == nil {
		return 0
	}
	return m.approvalQueue.Len()
}

func (m *model) stopBrowsing() {
	if !m.browsing {
		return
	}
	m.browsing, m.historyPos, m.draft = false, -1, ""
}

func (m *model) updateResponse(msg responseMsg) (tea.Model, tea.Cmd) {
	m.drainToolEvents()
	m.sending = false
	if m.cancelSend != nil {
		m.cancelSend()
		m.cancelSend = nil
	}
	switch {
	case m.stopping || errors.Is(msg.err, context.Canceled):
		m.stopping = false
		m.chatLog.Append(message{role: "tool", text: "⏹ stopped"})
	case msg.err != nil:
		m.err = msg.err
		m.chatLog.Append(message{role: "error", text: msg.err.Error()})
	default:
		m.usage = msg.usage
		m.hasUsage = true
		text := turn.AppendExecutionGuard(turn.AppendToolOutcomeGuard(msg.text, msg.failures), msg.evidence)
		answer := message{role: "assistant", text: text, meta: m.responseMeta(msg.usage)}
		if last := m.chatLog.Last(); last != nil && last.role == "assistant" && last.streaming {
			*last = answer
		} else {
			m.chatLog.Append(answer)
		}
	}
	m.syncChatViewport(true)
	return m, tea.Batch(m.notify.turnDone(), m.notifyPendingCmd())
}

// responseMeta assembles the provenance line for an answer that just landed.
// Nothing new is plumbed through the runner: the token count is already on the
// usage delta, and the elapsed time is the wall clock since the send started.
func (m *model) responseMeta(usage turn.TokenUsage) responseMeta {
	meta := responseMeta{tokens: usage.LatestCompletionTokens}
	if !m.sentAt.IsZero() {
		meta.elapsed = time.Since(m.sentAt)
	}
	if sw, ok := m.session.(modelSwitcher); ok {
		meta.model = sw.Model()
	}
	return meta
}

func (m *model) updateSpinner(msg spinner.TickMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	if m.sending {
		m.syncChatViewport(true)
		return m, tea.Batch(cmd, m.notifyPendingCmd())
	}
	return m, nil
}

func (m *model) updateToolEvents() (tea.Model, tea.Cmd) {
	m.drainToolEvents()
	m.syncChatViewport(true)
	if m.logPanel.enabled() {
		m.syncLogViewport()
	}
	return m, m.notifyPendingCmd()
}

func (m *model) drainToolEvents() {
	if m.toolEvents != nil {
		for _, ev := range m.toolEvents.Drain() {
			m.chatLog.ApplyEvent(ev)
		}
	}
	m.clampQueueCursor()
}

func (m *model) View() tea.View {
	content := m.mainView()
	if m.logsAvailable() && m.logPanel.mode == modeLogs {
		content = m.logsView()
	}
	// The popup floats over the finished frame, so the transcript underneath is
	// left exactly as it was and no height math changes.
	content = m.settingsView(m.overlay.Render(content, m.width, m.height))
	if banner := m.modeBanner(); banner != "" {
		content = banner + "\n" + content
	}
	// The frame is remembered unpainted: the highlight is decoration, and a
	// copy has to yield the text underneath it.
	m.frame = content
	content = m.selection.Paint(content, selectionStyle())
	// Alt screen keeps the whole UI inside a screen Bubble Tea owns. Inline
	// rendering only repaints the bottom terminal-height lines, so scrollback
	// showed pre-launch shell output and cropped older messages.
	v := tea.NewView(content)
	v.AltScreen = true
	// Mouse tracking costs the terminal its own selection, so the drag is
	// handled here instead (see mouse.go) and /mouse off hands it back.
	v.MouseMode = m.mouseMode.teaMode()
	return v
}

func (m *model) mainView() string {
	m.syncChatViewport(false)
	content := m.chat.View()
	// The popup goes above the keys, not between them and the input: it is a
	// list of what you are typing, so it belongs next to the transcript, and
	// the keys stay put on the row above the prompt.
	if popup := m.completion.view(); popup != "" {
		content += "\n" + popup
	}
	if footer := m.footer(); footer != "" {
		content += "\n" + footer
	}
	content += "\n" + m.inputArea()
	content += "\n" + m.statusRow()
	if tabs := m.tabStrip(); tabs != "" {
		content = tabs + "\n" + content
	}
	return content
}

// inputArea is the bottom block: normally the prompt, or the /model or /syntax
// overlay while one is open. Both the view and the viewport height math go through here
// so the transcript never overlaps whichever one is showing.
func (m *model) inputArea() string {
	for _, picker := range []*listPicker{&m.modelPicker, &m.syntaxPicker, &m.skillPicker, &m.palette} {
		if picker.open {
			return picker.view(m.mainWidth())
		}
	}
	return inputBox(m.mainWidth()).Render(m.input.View())
}

// inputBox frames the prompt in a rounded border. It replaces the filled
// cursor-line background bubbles draws by default: the border marks where input
// goes without painting a slab of color across the terminal.
func inputBox(width int) lipgloss.Style {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ui.Invokr.Palette.Brand).Padding(0, 1).Width(max(20, width-2))
}

// footer is the row above the input: the keybindings, or the one key that
// brings them back. The context readout used to share it; it sits on the
// statusline under the input now, where it is out of the way of the keys.
func (m *model) footer() string {
	hints := m.helpHint()
	if hints == "" || !m.hintsCollapsed() {
		return hints
	}
	// Collapsed, the hint is a single marker: it rides the right edge so it
	// reads as a corner affordance instead of a stray label under the chat.
	return joinEnds("", hints, m.mainWidth())
}

// joinEnds puts left against the left edge and right against the right edge of
// one row, and gives them a row each when they cannot share one.
func joinEnds(left, right string, width int) string {
	if right == "" {
		return left
	}
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 && left != "" {
		return left + "\n" + right
	}
	return left + strings.Repeat(" ", max(gap, 0)) + right
}

// hintsCollapsed reports whether the footer's hint cell is the one-key
// affordance rather than the full keybinding block or the quit confirmation.
func (m *model) hintsCollapsed() bool {
	return m.hideHints && !m.help.ShowAll && time.Since(m.quitArmed) >= quitConfirmWindow
}

// statusRow is the row under the input: where the agent is working on the
// left, how full the context window is on the right.
func (m *model) statusRow() string {
	width := m.mainWidth()
	return joinEnds(m.statusline.view(m.modelName(), reasoningLabel(m.session), string(m.policy.Mode()), width), m.contextStatus(width), width)
}

// quitConfirmWindow is how long an armed ctrl+c stays armed before a second
// press means "quit" again rather than "confirm quit".
const quitConfirmWindow = 2 * time.Second

// helpHint is the footer's left cell: the full keybinding row, or a single
// affordance when there is no room for it (a narrow pane) or no appetite for it
// (ctrl+g). The affordance is never dropped entirely — a footer with no keys on
// it is one the user cannot get back out of.
func (m *model) helpHint() string {
	if time.Since(m.quitArmed) < quitConfirmWindow {
		return ui.Invokr.GradientText("press ctrl+c again to exit")
	}
	if m.focusMode {
		return focusHint(m.keys)
	}
	// The copy notice lives as long as the highlight it belongs to: whatever
	// takes the selection down takes the message with it, so it never goes
	// stale and needs no timer.
	if m.copiedChars > 0 && !m.selection.Empty() && time.Since(m.copiedAt) < copyNoticeWindow {
		return ui.Invokr.GradientText(fmt.Sprintf("copied %d chars to clipboard", m.copiedChars))
	}
	if m.hideHints && !m.help.ShowAll {
		return hintAffordance(m.keys.ToggleHints, "show hints")
	}
	if m.mainWidth() < compactHelpWidth && !m.help.ShowAll {
		return hintAffordance(m.keys.Help, "help")
	}
	return strings.TrimSpace(m.help.View(m.keys))
}

// setHideHints records the choice on disk as well as in the model: ctrl+g is a
// standing preference ("I know the keys"), not a per-session one, so it has to
// survive a restart.
func (m *model) setHideHints(hide bool) {
	if m.hideHints == hide {
		return
	}
	m.hideHints = hide
	m.savePrefs()
}

func hintAffordance(binding key.Binding, label string) string {
	// Warn is the theme's yellow: the collapsed footer is one key on an
	// otherwise empty row, and brand blue reads as decoration next to the
	// blue-bordered input right below it.
	return ui.Invokr.Warn.Render(binding.Help().Key) + " " + ui.Invokr.Muted.Render(label)
}

// contextStatus is the statusline's right cell, empty only until a turn has
// reported usage. It has no hide toggle: the readout is one cell on a row that
// already exists, not the two full rows above the input it used to occupy.
func (m *model) contextStatus(width int) string {
	if !m.hasUsage {
		return ""
	}
	return renderContextStatus(m.usage, width, compactionThreshold(m.session, m.usage.ContextWindowTokens))
}

func (m *model) chatContent() string {
	var b strings.Builder

	b.WriteString(renderIntro(m.mainWidth(), m.height) + "\n\n")

	// Fold hit-testing and focus scrolling need each block's content-line
	// range, so the transcript render records them, shifted past the intro.
	offset := strings.Count(b.String(), "\n")
	content, ranges := m.chatLog.ContentRanges(m.mainWidth(), m.spinner.View(), m.focusedMsg())
	for i := range ranges {
		ranges[i].start += offset
		ranges[i].end += offset
	}
	m.blockRanges = ranges
	b.WriteString(content)

	if m.sending {
		if m.pendingApprovals() > 0 {
			b.WriteString(renderRunning(m.spinner.View(), "waiting for approval", ui.Invokr.Warn, m.sentAt, m.mainWidth()) + "\n\n")
		} else {
			b.WriteString(renderThinking(m.spinner.View(), m.sentAt, m.mainWidth()) + "\n\n")
		}
	}
	if m.approvalQueue != nil {
		if items := m.approvalQueue.Snapshot(); len(items) > 0 {
			b.WriteString(renderApprovalQueuePanel(items, m.approvals.cursor, m.approvals.expanded, m.approvals.detailOffset, m.keys, m.help, m.mainWidth()) + "\n")
			if m.input.Value() != "" {
				b.WriteString("  " + ui.Invokr.Muted.Render("input has text: decision keys are off until it is sent or cleared (ctrl+u)") + "\n")
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// compactHelpWidth leaves room for the input and chat panes on narrow terminals.
const compactHelpWidth = 100

func (m *model) submit() (*model, tea.Cmd) {
	if m.pendingApprovals() > 0 || m.logPanel.mode == modeLogs {
		return m, nil
	}

	// Enter accepts the highlighted suggestion, but only for slash commands:
	// `/h` + enter means "run /help". An `@file` suggestion is left alone —
	// enter there must send the message exactly as the user typed it.
	if m.completion.active() && strings.HasPrefix(m.completion.token, "/") {
		m.input.SetValue(strings.TrimSuffix(m.completion.selected(m.input.Value()), " "))
		m.input.CursorEnd()
	}
	// The universal multiline fallback: a line ending in a backslash continues
	// on the next one, for terminals that deliver none of the newline chords.
	if strings.HasSuffix(m.input.Value(), "\\") {
		m.input.SetValue(strings.TrimSuffix(m.input.Value(), "\\") + "\n")
		m.input.MoveToEnd()
		return m, nil
	}
	m.completion.dismiss()
	text := strings.TrimSpace(m.input.Value())
	if cmd, handled := m.runCommand(text); handled {
		return m, cmd
	}
	if m.sending {
		return m, nil
	}
	m.input.Reset()
	m.browsing, m.historyPos, m.draft = false, -1, ""

	if text == "" {
		return m, nil
	}
	if text == "exit" || text == "quit" {
		return m, m.quitCmd()
	}
	// "/help" (with slash) is already handled by runCommand above; only the
	// bare "help" alias (no slash, so runCommand never sees it) lands here.
	if text == "help" {
		return m.showHelp()
	}

	m.appendHistory(text)
	m.lastPrompt = text
	m.chatLog.Append(message{role: "user", text: text})
	m.sending = true
	m.sentAt = time.Now()
	m.userScrolled = false
	m.syncChatViewport(true)

	sendCtx, cancel := context.WithCancel(m.ctx)
	m.cancelSend = cancel
	return m, tea.Batch(func() tea.Msg {
		resp, err := m.session.Send(sendCtx, text)
		return responseMsg{text: resp.Message, usage: resp.Usage, failures: resp.ToolFailures, evidence: resp.ExecutionEvidence, err: err}
	}, m.persistHistoryCmd(text), m.spinner.Tick)
}

type contextCompactor interface{ RequestCompaction() }

func requestContextCompaction(session turn.Session) {
	if compactor, ok := session.(contextCompactor); ok {
		compactor.RequestCompaction()
	}
}

// modelName is the model the next turn will use, or "" when the session does
// not expose one.
func (m *model) modelName() string { return currentModel(m.session) }

// currentModel is the model a session will use next, or "" when the session
// does not expose one.
func currentModel(session turn.Session) string {
	if sw, ok := session.(modelSwitcher); ok {
		return sw.Model()
	}
	return ""
}

type modelSwitcher interface {
	SetModel(string)
	Model() string
}

type modelLister interface {
	ListModels(context.Context) ([]string, error)
}

func switchModelOnSession(session turn.Session, name string) {
	if sw, ok := session.(modelSwitcher); ok {
		sw.SetModel(name)
	}
}

type modelListMsg struct {
	models  []string
	current string
	err     error
}

func fetchModelList(ctx context.Context, session turn.Session) modelListMsg {
	current := currentModel(session)
	lister, ok := session.(modelLister)
	if !ok {
		return modelListMsg{current: current}
	}
	models, err := lister.ListModels(ctx)
	return modelListMsg{models: models, current: current, err: err}
}

// runModelCommand is /model: a name switches the session's model, a bare
// command fetches the provider's list for the picker.
func runModelCommand(m *model, text string, _ []string) tea.Cmd {
	parts := strings.SplitN(text, " ", 2)
	if len(parts) < 2 {
		ctx, sess := m.ctx, m.session
		return func() tea.Msg { return fetchModelList(ctx, sess) }
	}
	name := strings.TrimSpace(parts[1])
	feedback := "usage: /model <name>"
	switch {
	case name == "":
	case m.sending:
		switchModelOnSession(m.session, name)
		feedback = "model switch to " + name + " queued — applies next turn"
	default:
		switchModelOnSession(m.session, name)
		feedback = "model set to " + name + " (applies from next turn)"
	}
	m.chatLog.Append(message{role: "tool", text: feedback})
	m.syncChatViewport(true)
	return nil
}

func (m *model) handleModelListMsg(msg modelListMsg) (*model, tea.Cmd) {
	if m.settings.open {
		m.settings.models = msg.models
		return m, m.buildSettingsTab(1)
	}
	switch {
	case msg.err != nil:
		m.commandMessage("error fetching models: " + msg.err.Error())
	case len(msg.models) == 0:
		// Nothing to pick from, so fall back to reporting what is in use.
		text := "(provider returned no model list)"
		if msg.current != "" {
			text = "current: " + msg.current + "\n" + text
		}
		m.commandMessage(text)
	default:
		m.modelPicker.showModels(msg.models, msg.current)
	}
	m.syncChatViewport(true)
	return m, nil
}

func (m *model) updateChatViewportKey(msg tea.KeyPressMsg) bool {
	if m.logPanel.mode == modeLogs {
		return false
	}

	m.syncChatViewport(false)
	switch {
	case key.Matches(msg, m.keys.PageUp, m.keys.PageDn):
		m.chat, _ = m.chat.Update(msg)
	case key.Matches(msg, m.keys.ToggleHints):
		// Collapsing the hints frees a line at some widths, so the viewport is
		// resized below.
		m.setHideHints(!m.hideHints)
	case key.Matches(msg, m.keys.ScrollUp):
		m.chat.ScrollUp(scrollStep)
	case key.Matches(msg, m.keys.ScrollDown):
		m.chat.ScrollDown(scrollStep)
	case key.Matches(msg, m.keys.Home):
		m.chat.GotoTop()
	case key.Matches(msg, m.keys.End):
		m.chat.GotoBottom()
	default:
		return false
	}
	// Once the user scrolls away from the bottom, stop auto-following new output
	// so spinner/log ticks can't yank the view back down; resume following only
	// when they return to the bottom (End, or paging down to it).
	m.syncChatViewport(false)
	m.userScrolled = !m.chat.AtBottom()
	return true
}

// scrollStep is how many lines one wheel notch or shift+arrow moves the chat.
const scrollStep = 3

func (m *model) updateMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if m.logPanel.mode == modeLogs {
		m.syncLogViewport()
		switch msg.Button {
		case tea.MouseWheelUp:
			m.logPanel.viewport.ScrollUp(scrollStep)
		case tea.MouseWheelDown:
			m.logPanel.viewport.ScrollDown(scrollStep)
		}
		return m, nil
	}
	m.syncChatViewport(false)
	switch msg.Button {
	case tea.MouseWheelUp:
		m.chat.ScrollUp(scrollStep)
	case tea.MouseWheelDown:
		m.chat.ScrollDown(scrollStep)
	default:
		return m, nil
	}
	m.userScrolled = !m.chat.AtBottom()
	return m, nil
}

func (m *model) syncChatViewport(forceBottom bool) {
	followViewport(&m.chat, m.mainWidth(), m.chatVisibleLines(), m.chatContent(), forceBottom && !m.userScrolled)
}

// followViewport re-lays a viewport out around new content and keeps it pinned
// to the bottom when it was already there — or when the caller insists, which
// is what makes new output follow while a scrolled-back reader stays put.
func followViewport(v *viewport.Model, width, height int, content string, forceBottom bool) {
	atBottom := v.AtBottom()
	v.SetWidth(width)
	v.SetHeight(height)
	v.SetContent(content)
	if forceBottom || atBottom || v.PastBottom() {
		v.GotoBottom()
	}
}

func (m *model) chatVisibleLines() int {
	// An empty footer or popup occupies no row at all; the input and the status
	// row always occupy at least one.
	rows := func(text string) int { return strings.Count(text, "\n") + 1 }
	optional := func(text string) int {
		if text == "" {
			return 0
		}
		return rows(text)
	}
	chrome := lipgloss.Height(m.tabStrip()) + optional(m.modeBanner()) + optional(m.footer()) + optional(m.completion.view()) + max(1, rows(m.inputArea())) + rows(m.statusRow())
	return max(1, m.height-chrome)
}

func (m *model) showHelp() (*model, tea.Cmd) {
	// ShowAll outranks the ctrl+g preference (see helpHint), so asking for help
	// while the hints are hidden draws them for as long as the popup is up
	// without touching the stored preference: dismissing it hides them again.
	m.help.ShowAll = !m.help.ShowAll
	// The command table goes in the popup rather than the transcript, so it no
	// longer piles up a copy per press; esc takes it back down.
	m.overlay.Open("help", renderCommandHelp(m.overlayContentWidth()))
	m.syncChatViewport(true)
	return m, nil
}
