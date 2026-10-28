package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/capability/sessionstore"
	"github.com/invokr-agent/invokr/cmd/agentui"
	"github.com/invokr-agent/invokr/platform/turn"
	toolcore "github.com/invokr-agent/invokr/tools/core"

	appruntime "github.com/invokr-agent/invokr/platform/runtime"

	"go.uber.org/zap"

	"github.com/spf13/cobra"
)

var runAgentUI = agentui.Run

type ServicesProvider func() (appruntime.Services, error)

func NewAgentCmd(services ServicesProvider) *cobra.Command {
	var resumeID string
	var last bool
	cmd := &cobra.Command{
		Use:     "agent",
		Short:   "Run the interactive agent (what bare `invokr` does)",
		Long:    "Opens the TUI: you type, the agent answers, and tool calls stop for\napproval unless a standing grant covers them. The transcript is stored,\nso --resume can pick it up later.",
		Example: "  invokr\n  invokr --resume            # pick a session from a menu\n  invokr --resume <id>       # resume that session\n  invokr --last              # resume the latest session here",
		GroupID: groupRun,
		RunE: func(cmd *cobra.Command, args []string) error {
			runtimeServices, err := services()
			if err != nil {
				return err
			}

			id := resumeIDFromArgs(resumeID, args)
			if last {
				id = resumeLastSentinel
			}
			session, approvals, transcript, err := resumeAgentSession(cmd, runtimeServices, id)
			if errors.Is(err, agentui.ErrPickCanceled) {
				return nil // dismissing the picker leaves, it does not start fresh
			}
			if err != nil {
				return err
			}
			return runAgentSessions(cmd.Context(), runtimeServices, agentui.Params{
				Session: session, Commands: runtimeServices.Commands, ServerNotice: runtimeServices.ServerProblem,
				EventBus: runtimeServices.Events, Approvals: approvals, Grants: runtimeServices.Grants,
				Logs: runtimeServices.Logs, LogView: runtimeServices.LogView,
				PromptHistory:     runtimeServices.PromptHistory,
				PrefsDir:          runtimeServices.App.Memory.Dir,
				SyntaxTheme:       runtimeServices.App.UI.SyntaxTheme,
				NotifyMode:        runtimeServices.App.UI.Notify,
				Transcript:        transcript,
				RecordInterrupted: interruptRecorder(cmd.Context(), runtimeServices),
				In:                cmd.InOrStdin(), Out: cmd.OutOrStdout(), ErrOut: cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().StringVar(&resumeID, "resume", "", "Resume a stored session by ID; pass no ID (or \"pick\") to choose one from a menu")
	// NoOptDefVal lets --resume stand alone. pflag then stops binding a
	// space-separated value, so `--resume <id>` arrives as a positional instead;
	// resumeIDFromArgs recovers it and keeps that spelling working.
	cmd.Flags().Lookup("resume").NoOptDefVal = resumePickSentinel
	cmd.Flags().BoolVar(&last, "last", false, "Resume the most recent stored session in this directory")
	cmd.MarkFlagsMutuallyExclusive("resume", "last")
	cmd.Args = resumeAwareArgs(&resumeID)
	return cmd
}

func runAgentSessions(ctx context.Context, services appruntime.Services, params agentui.Params) error {
	defer func() {
		if closer, ok := params.Session.(interface{ Close() }); ok {
			closer.Close()
		}
	}()
	for {
		registerCustomCommands(ctx, services.Commands, services.Logger)
		if err := runAgentUI(ctx, params); !errors.Is(err, agentui.ErrNewSession) {
			return err
		}
		if services.NewAgentSession == nil {
			return fmt.Errorf("runtime does not support starting a new session")
		}
		next, err := services.NewAgentSession()
		if err != nil {
			return err
		}
		if closer, ok := params.Session.(interface{ Close() }); ok {
			closer.Close()
		}
		services = next
		params.Session, params.Approvals, params.Commands = next.Session, next.Approvals, next.Commands
		params.Grants = next.Grants
		params.Transcript = []turn.TranscriptMessage{{Role: "tool", Text: "new session started — saved history and memory kept"}}
	}
}

// interruptRecorder saves a prompt the user stopped before it was answered,
// with an interrupted marker, so a resumed session shows it rather than
// dropping it: the store is otherwise written only when a turn answers.
func interruptRecorder(ctx context.Context, services appruntime.Services) func(sessionID, prompt string) {
	return func(sessionID, prompt string) {
		store := sessionstore.NewForWorkspace(services.App.Memory.Dir, services.App.Workspace.Root, services.Logger)
		ctx := context.WithoutCancel(ctx)
		_, err := store.Append(ctx, sessionID, "user", prompt)
		if err == nil {
			_, err = store.AppendInterrupted(ctx, sessionID, "assistant", "", sessionstore.ToolRecord{})
		}
		if err != nil {
			services.Logger.Warn("session store: could not save the interrupted prompt", zap.Error(err))
		}
	}
}

// resumePickSentinel is the value pflag assigns when --resume is given without
// one. Session IDs are UUIDs, so "pick" cannot collide with a real one — and
// unlike an unprintable sentinel it renders as `--resume string[="pick"]` in
// help, making `--resume pick` a documented spelling rather than a leaked
// implementation detail.
const resumePickSentinel = "pick"

// resumeLastSentinel stands for --last; the NUL keeps it untypeable as an ID.
const resumeLastSentinel = "\x00last"

// resumeAwareArgs allows exactly one positional after a bare --resume, so
// `--resume <id>` still works despite NoOptDefVal, while every other stray
// argument keeps failing as a mistyped subcommand.
func resumeAwareArgs(resumeID *string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if *resumeID == resumePickSentinel && len(args) == 1 {
			return nil
		}
		return cobra.NoArgs(cmd, args)
	}
}

func resumeIDFromArgs(resumeID string, args []string) string {
	if resumeID == resumePickSentinel && len(args) == 1 {
		return args[0]
	}
	return resumeID
}

// purgeSession deletes a session everywhere it is stored: the transcript and
// its title sidecar, plus the curated records and raw events the memory tier
// keyed to it. Delete means gone, not just hidden from the picker.
func purgeSession(cmd *cobra.Command, services appruntime.Services, store *sessionstore.Store) func(string) error {
	return func(id string) error {
		if err := store.Delete(id); err != nil {
			return err
		}
		purger, ok := services.Memory.(interface {
			DeleteSession(context.Context, string) error
		})
		if !ok {
			return nil
		}
		return purger.DeleteSession(cmd.Context(), id)
	}
}

func resolveResumeID(cmd *cobra.Command, services appruntime.Services, store *sessionstore.Store, resumeID string) (string, error) {
	switch resumeID {
	case resumeLastSentinel:
		last, err := store.LastResumableInCwd()
		return last.ID, err
	case resumePickSentinel:
	default:
		return resumeID, nil
	}
	return agentui.PickSession(cmd.Context(), store, purgeSession(cmd, services, store), cmd.InOrStdin(), cmd.OutOrStdout())
}

func resumeAgentSession(cmd *cobra.Command, services appruntime.Services, resumeID string) (turn.Session, *approval.Broker, []turn.TranscriptMessage, error) {
	if resumeID == "" {
		return services.Session, services.Approvals, nil, nil
	}
	store := sessionstore.NewForWorkspace(services.App.Memory.Dir, services.App.Workspace.Root, services.Logger)
	resumeID, err := resolveResumeID(cmd, services, store, resumeID)
	if err != nil {
		return nil, nil, nil, err
	}
	if resumeID == "" {
		// Picker dismissed: start fresh rather than failing the invocation the
		// user is already sitting in front of.
		return services.Session, services.Approvals, nil, nil
	}
	stored, err := store.Load(cmd.Context(), resumeID)
	if err != nil {
		return nil, nil, nil, err
	}
	session, approvals, transcript, err := attachStoredTranscript(services, resumeID, stored)
	if err != nil {
		return nil, nil, nil, err
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "resumed session %s\n", resumeID)
	return session, approvals, transcript, nil
}

// attachStoredTranscript opens the stored session under its own ID and replays
// the transcript into it. Continuing the session — rather than starting a new
// one seeded with a text summary of the old — is what keeps a resumed run a
// second segment of one session instead of a fork.
func attachStoredTranscript(services appruntime.Services, sessionID string, stored []sessionstore.Message) (turn.IdentifiedSession, *approval.Broker, []turn.TranscriptMessage, error) {
	if services.NewChatSession == nil {
		return nil, nil, nil, fmt.Errorf("runtime does not support resumable sessions")
	}
	session, approvals, err := services.NewChatSession(sessionID)
	if err != nil {
		return nil, nil, nil, err
	}
	transcript := toolcore.MapSlice(stored, func(item sessionstore.Message) turn.TranscriptMessage {
		return turn.TranscriptMessage{Role: item.Role, Text: item.Text, Interrupted: item.Interrupted,
			ToolCalls: item.ToolCalls, ToolCallCount: item.ToolCallCount, ExecutionEvidence: item.ExecutionEvidence}
	})
	resumer, ok := session.(turn.TranscriptResumer)
	if !ok {
		return nil, nil, nil, fmt.Errorf("runtime session does not support transcript resume")
	}
	if err := resumer.ResumeTranscript(transcript); err != nil {
		return nil, nil, nil, err
	}
	return session, approvals, transcript, nil
}

// registerCustomCommands surfaces tool- and skill-declared slash commands in
// the TUI registry. Built-in names win; a shadowed dynamic command is logged
// so its author can see why it never appears in /help.
func registerCustomCommands(ctx context.Context, backend appruntime.CommandBackend, logger *zap.Logger) {
	if backend == nil {
		return
	}
	logger = cmp.Or(logger, zap.NewNop())
	register := func() {
		dynamic := toolcore.MapSlice(backend.CustomCommands(ctx), func(command appruntime.CustomCommand) agentui.DynamicCommand {
			return agentui.DynamicCommand(command)
		})
		for _, name := range agentui.RegisterDynamicCommands(dynamic) {
			logger.Warn("dynamic command shadowed by an existing command, skipping", zap.String("command", name))
		}
	}
	register()
	agentui.RefreshDynamicCommands = register
}
