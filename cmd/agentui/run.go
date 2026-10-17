package agentui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/support/fsutil"

	tea "charm.land/bubbletea/v2"
)

type LogSource interface {
	Lines() []string
}

// ErrNewSession asks the CLI owner to reopen the UI with a fresh session.
var ErrNewSession = errors.New("new session requested")

type Params struct {
	Session       turn.Session
	Commands      CommandBackend
	EventBus      events.Bus
	Approvals     *approval.Broker
	Grants        *approval.Store
	Logs          LogSource
	LogView       bool
	PromptHistory turn.PromptHistory
	// PrefsDir is where the TUI keeps its view preferences. Empty disables
	// persistence, which is what the plain and test paths want.
	PrefsDir string
	// SyntaxTheme is config's ui.syntax_theme. Empty matches the terminal
	// background; a stylesheet picked with /syntax shadows it.
	SyntaxTheme string
	// NotifyMode is config's ui.notify. A mode picked with /notify shadows it.
	NotifyMode  string
	Transcript  []turn.TranscriptMessage
	In          io.Reader
	Out, ErrOut io.Writer
}

func validateRunArgs(p Params) error {
	switch {
	case p.Session == nil:
		return fmt.Errorf("agent session is required")
	case p.In == nil:
		return fmt.Errorf("input is required")
	case p.Out == nil:
		return fmt.Errorf("output is required")
	case p.EventBus == nil:
		return fmt.Errorf("event bus is required")
	case p.PromptHistory == nil:
		return fmt.Errorf("prompt history is required")
	}
	return nil
}

func Run(ctx context.Context, p Params) error {
	if err := validateRunArgs(p); err != nil {
		return err
	}

	if !fsutil.IsTerminal(p.In) {
		return runPlainWithCommands(ctx, p.Session, p.Commands, p.Approvals, p.Grants, p.EventBus, p.In, p.Out, p.ErrOut)
	}

	toolEvents := newToolEventQueue()
	initialHistory := loadHistory(ctx, p.PromptHistory)
	var approvalQueue *approval.ApprovalQueue
	if p.Approvals != nil {
		approvalQueue = approval.NewApprovalQueue(ctx, p.Approvals)
		approvalQueue.SetStandingGrants(p.Grants)
	}
	uiModel := newModelWithCommands(ctx, p.Session, p.Commands, toolEvents, approvalQueue, p.Logs, p.LogView, p.PromptHistory, initialHistory)
	for _, item := range p.Transcript {
		uiModel.appendSavedTranscript(item)
	}
	uiModel.grants = p.Grants
	uiModel.prefsPath = prefsPath(p.PrefsDir)
	saved := loadPrefs(uiModel.prefsPath)
	uiModel.hideHints = saved.HideHints
	uiModel.mouseMode, _ = parseMouseMode(saved.MouseMode)
	uiModel.notify.mode, _ = parseNotifyMode(cmp.Or(saved.NotifyMode, p.NotifyMode))
	setSyntaxTheme(cmp.Or(saved.SyntaxTheme, p.SyntaxTheme))
	defer func() {
		if approvalQueue != nil {
			approvalQueue.Close()
		}
	}()
	program := tea.NewProgram(uiModel, tea.WithContext(ctx), tea.WithInput(p.In), tea.WithOutput(p.Out))
	unregister := registerToolEvents(p.EventBus, p.Session, toolEvents.Push, func() {
		program.Send(toolEventsReadyMsg{})
	})
	defer unregister()
	finalModel, err := program.Run()
	if err != nil {
		return err
	}
	if got, ok := finalModel.(*model); ok && got.err != nil {
		return got.err
	}
	return nil
}
