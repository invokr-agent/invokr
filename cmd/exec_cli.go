package cmd

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/capability/sessionstore"
	"github.com/invokr-ai/invokr/cmd/execwire"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/runtime"
	"github.com/invokr-ai/invokr/platform/turn"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

type execOptions struct {
	jsonOutput, failOnToolError, lastSession, allowAll bool
	outputPath, schemaPath, resumeID                   string
	allowTiers                                         []string
}

// NewExecCmd takes appConfig so --max-turns can bind straight into the config
// the runtime is bootstrapped from: cobra parses flags before the root's
// PersistentPreRunE builds the services, so the override is in place by then.
func NewExecCmd(services ServicesProvider, appConfig *config.AppConfig) *cobra.Command {
	opts := execOptions{}
	cmd := &cobra.Command{
		Use:     "exec [prompt|-]",
		Aliases: []string{"run"},
		Short:   "Run the agent non-interactively",
		Long:    "The same agent as the TUI, without a terminal: one prompt in, one\nanswer out, exit code 0. Reads the prompt from the argument, or from\nstdin when it is '-' or absent.\n\nTool approvals auto-DENY here, because nobody is watching. Opt in per\ntier with --allow, or to everything with --allow-all.\n\n--resume/--last continue a stored session, spelled the same as on the\ninteractive agent.",
		Example: "  invokr exec \"list the failing tests\"\n  git diff | invokr exec -                       # prompt on stdin\n  invokr exec --json \"...\" | jq .                # JSONL event ledger\n  invokr exec --allow read-only \"...\"            # let it read the workspace\n  invokr exec --last \"and now write the fix\"     # continue the last session",
		GroupID: groupRun,
		Args:    cobra.MaximumNArgs(1),
		// A tool-failure exit (--fail-on-tool-error) surfaces as a RunE error;
		// don't frame that normal signal as a usage error.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Catches the retired `exec resume <id> "prompt"` grammar's bare
			// form (longer forms already fail on MaximumNArgs(1)) so it errors
			// instead of silently running the agent with the literal prompt
			// "resume".
			if len(args) == 1 && args[0] == "resume" && opts.resumeID == "" && !opts.lastSession {
				return fmt.Errorf("invokr exec resume is retired; use --resume <session-id> or --last")
			}
			runtimeServices, err := services()
			if err != nil {
				return err
			}
			if opts.lastSession || opts.resumeID != "" {
				return runExecResume(cmd.Context(), runtimeServices, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args, opts)
			}
			return runExec(cmd.Context(), runtimeServices, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args, opts)
		},
	}
	registerExecFlags(cmd, &opts, appConfig)
	return cmd
}

func registerExecFlags(cmd *cobra.Command, opts *execOptions, appConfig *config.AppConfig) {
	cmd.Flags().IntVar(&appConfig.Agent.MaxTurns, "max-turns", appConfig.Agent.MaxTurns, "Turn budget for this run, overriding max_turns (also INVOKR_AGENT_MAX_TURNS); read-only turns cost less and a progressing run may be extended, so it is a budget, not a call count")
	cmd.Flags().BoolVar(&opts.jsonOutput, "json", false, "Emit one JSON object per line: tool events, then the final response")
	cmd.Flags().StringVarP(&opts.outputPath, "output-last-message", "o", "", "Also write the final assistant response to this file")
	cmd.Flags().StringVar(&opts.schemaPath, "output-schema", "", "Path to a JSON Schema: it is appended to the prompt, and the run fails unless the final response parses as JSON")
	cmd.Flags().BoolVar(&opts.failOnToolError, "fail-on-tool-error", false, "Exit nonzero for failed, denied, or flagged tool results (including nonzero execution exits), even if the agent answered")
	cmd.Flags().StringSliceVar(&opts.allowTiers, "allow", nil, "Auto-approve one tool tier, repeatable; \"read-only\" covers the read-only workspace tiers")
	cmd.Flags().BoolVar(&opts.allowAll, "allow-all", false, "Auto-approve every tool call — unattended arbitrary code execution, so scope the workspace first")
	cmd.Flags().StringVar(&opts.resumeID, "resume", "", "Resume a stored session by ID, as listed by 'invokr sessions'")
	cmd.Flags().BoolVar(&opts.lastSession, "last", false, "Resume the most recent stored session")
}

func runExec(ctx context.Context, s runtime.Services, in io.Reader, out io.Writer, errOut io.Writer, args []string, opts execOptions) error {
	prompt, err := execPrompt(in, args)
	if err != nil {
		return err
	}
	return runExecPrompt(ctx, s, out, errOut, prompt, opts)
}

func runExecResume(ctx context.Context, s runtime.Services, in io.Reader, out io.Writer, errOut io.Writer, args []string, opts execOptions) error {
	store := sessionstore.NewForWorkspace(s.App.Memory.Dir, s.App.Workspace.Root, s.Logger)
	sessionID, promptArgs, err := resolveResumeTarget(store, opts, args)
	if err != nil {
		return err
	}
	history, err := store.Load(ctx, sessionID)
	if err != nil {
		return err
	}
	prompt, err := execPrompt(in, promptArgs)
	if err != nil {
		return err
	}
	// Continue the stored session itself: the run keeps its ID and appends a
	// second segment to the same transcript. Replaying the messages also means
	// no "previous transcript" preamble is pasted into the prompt — which is
	// what used to nest, one layer deeper on every resume.
	session, approvals, _, err := attachStoredTranscript(s, sessionID, history)
	if err != nil {
		return err
	}
	s.Session, s.Approvals = session, approvals
	if err := reportResumeTarget(s, out, errOut, sessionID, history, opts); err != nil {
		return err
	}
	return runExecPrompt(ctx, s, out, errOut, prompt, opts)
}

// reportResumeTarget names the session the run attached to and whether it was
// interrupted. `--last` picks the session on its own, so without this the
// caller cannot tell a resume of the killed run from a resume of some unrelated
// finished one in the same directory.
func reportResumeTarget(s runtime.Services, out io.Writer, errOut io.Writer, sessionID string, history []sessionstore.Message, opts execOptions) error {
	status := "completed"
	if len(history) > 0 && history[len(history)-1].Interrupted {
		status = "interrupted"
	}
	if s.Logger != nil {
		s.Logger.Info("resuming session", zap.String("session_id", sessionID), zap.String("status", status))
	}
	if !opts.jsonOutput {
		_, err := fmt.Fprintf(errOut, "resuming session %s (%s)\n", sessionID, status)
		return err
	}
	return writeJSONLine(out, map[string]any{execwire.KeyType: execwire.TypeThreadStarted, "thread_id": sessionID, "status": status})
}

// resolveResumeTarget picks the session to resume. Every remaining argument is
// prompt: the session is named by a flag, never positionally.
func resolveResumeTarget(store *sessionstore.Store, opts execOptions, args []string) (string, []string, error) {
	if opts.resumeID != "" {
		return opts.resumeID, args, nil
	}
	if !opts.lastSession {
		return "", nil, fmt.Errorf("resume requires --last or --resume <session-id>")
	}
	last, err := store.LastResumableInCwd()
	if err != nil {
		return "", nil, err
	}
	return last.ID, args, nil
}

func runExecPrompt(ctx context.Context, s runtime.Services, out io.Writer, errOut io.Writer, prompt string, opts execOptions) error {
	sessionID := s.Session.SessionID()
	stopEvents := registerExecEvents(s.Events, out, errOut, sessionID, opts.jsonOutput)
	defer stopEvents()
	if opts.allowAll {
		_, _ = fmt.Fprintln(errOut, newRenderer(errOut).danger("WARNING: --allow-all enables unattended arbitrary tool execution"))
	}
	stopApprovals := serveExecApprovals(s.Approvals, s.Grants, newExecApprovalPolicy(opts))
	defer stopApprovals()
	if opts.schemaPath != "" {
		schemaBytes, err := os.ReadFile(opts.schemaPath)
		if err != nil {
			return fmt.Errorf("read output schema: %w", err)
		}
		prompt += "\n\nFinal response must be valid JSON matching this schema:\n" + string(schemaBytes)
	}
	// The transcript is persisted by the shared memory event handler on
	// EventAfterResponse (see internal/memory/manager.go), so exec no longer
	// writes to the session store directly — except when the run never reaches
	// a response, which is what progress is for.
	progress := &execProgress{}
	defer progress.register(s.Events, sessionID)()
	resp, err := s.Session.Send(ctx, prompt)
	if err != nil {
		progress.persistInterrupted(ctx, s, sessionID, prompt)
		return err
	}
	return completeExecResponse(out, sessionID, s.App.Provider.Model, resp, opts)
}

// completedEnvelope is the thread.completed ledger record: the outcome ledger
// (ADR 0001), not the model's prose, is what a reader scores the run from. The
// response knows the model the run actually asked for; config is only the
// fallback for a run that never reached the provider.
func completedEnvelope(sessionID, model, message string, resp turn.AgentResponse) map[string]any {
	envelope := map[string]any{execwire.KeyType: execwire.TypeThreadCompleted, execwire.KeyModel: cmp.Or(resp.Model, model), "thread_id": sessionID, "message": message, "tool_failures": resp.ToolFailures, "tool_call_count": resp.ToolCallCount, "usage": resp.Usage}
	envelope["execution_evidence"], envelope["tool_flagged"] = resp.ExecutionEvidence, turn.FlaggedExecution(resp.ExecutionEvidence)
	if resp.StopReason != "" {
		envelope["stop_reason"] = resp.StopReason
	}
	if len(resp.Retries) > 0 {
		envelope["retries"] = resp.Retries
	}
	if resp.ServedModel != "" {
		envelope[execwire.KeyServedModel] = resp.ServedModel
	}
	return envelope
}

func completeExecResponse(out io.Writer, sessionID string, model string, resp turn.AgentResponse, opts execOptions) error {
	message := resp.Message
	// Structured-output mode (--json) suppresses the human-facing guard in the
	// message; callers still get tool_failures/tool_call_count in the JSONL.
	if !opts.jsonOutput && opts.schemaPath == "" {
		message = turn.AppendExecutionGuard(turn.AppendToolOutcomeGuard(message, resp.ToolFailures), resp.ExecutionEvidence)
	}
	if err := validateExecOutput(message, opts.schemaPath); err != nil {
		return err
	}
	if opts.outputPath != "" {
		if err := os.WriteFile(opts.outputPath, []byte(message), 0600); err != nil {
			return fmt.Errorf("write final response: %w", err)
		}
	}
	if opts.jsonOutput {
		if err := writeJSONLine(out, completedEnvelope(sessionID, model, message, resp)); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintln(out, message); err != nil {
		return err
	}
	if count := len(resp.ToolFailures) + len(turn.FlaggedExecution(resp.ExecutionEvidence)); opts.failOnToolError && count > 0 {
		return fmt.Errorf("agent completed with %d failed or flagged tool result(s)", count)
	}
	return nil
}

func execPrompt(in io.Reader, args []string) (string, error) {
	if len(args) == 0 || args[0] == "-" {
		data, err := io.ReadAll(in)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	return strings.TrimSpace(args[0]), nil
}

func registerExecEvents(bus events.Bus, out io.Writer, errOut io.Writer, sessionID string, jsonOutput bool) func() {
	if bus == nil {
		return func() {}
	}
	return bus.Register(events.HandlerFunc(func(_ context.Context, event events.Event) error {
		if event.SessionID != sessionID {
			return nil
		}
		return handleExecEvent(out, errOut, event, jsonOutput)
	}))
}

func handleExecEvent(out io.Writer, errOut io.Writer, event events.Event, jsonOutput bool) error {
	if jsonOutput {
		return writeExecEvent(out, event)
	}
	// The progress trace goes to stderr so it never contaminates the answer on
	// stdout; painting it is what separates the two streams by eye when both
	// land in one terminal.
	r := newRenderer(errOut)
	if event.Tool != nil {
		switch event.Type {
		case events.EventToolStarted:
			_, _ = fmt.Fprintf(errOut, "%s %s\n", r.muted("tool started:"), event.Tool.Name)
		case events.EventToolCompleted:
			_, _ = fmt.Fprintf(errOut, "%s %s\n", r.success("tool completed:"), event.Tool.Name)
		case events.EventToolFailed:
			_, _ = fmt.Fprintf(errOut, "%s %s: %s\n", r.danger("tool failed:"), event.Tool.Name, event.Tool.Error)
		case events.EventToolProgress:
			_, _ = fmt.Fprintf(errOut, "%s %s: %s\n", r.muted("tool progress:"), event.Tool.Name, event.Tool.Progress)
		}
	}
	if event.Type == events.EventNotice && event.Notice != nil {
		_, _ = fmt.Fprintln(errOut, r.warn(event.Notice.Text))
	}
	return nil
}

// execItemWireTypes maps the tool lifecycle events onto their wire type; they
// all carry the same item payload, so only the label differs.
var execItemWireTypes = map[events.EventType]string{
	events.EventToolStarted: execwire.TypeItemStarted, events.EventToolCompleted: execwire.TypeItemCompleted,
	events.EventToolProgress: execwire.TypeItemProgress, events.EventToolFailed: execwire.TypeError}

func writeExecEvent(out io.Writer, event events.Event) error {
	if wire, ok := execItemWireTypes[event.Type]; ok {
		return writeJSONLine(out, map[string]any{execwire.KeyType: wire, execwire.KeyItem: event.Tool})
	}
	switch event.Type {
	case events.EventAfterResponse:
		return writeJSONLine(out, map[string]any{execwire.KeyType: execwire.TypeTurnCompleted})
	case events.EventNotice:
		return writeJSONLine(out, map[string]any{execwire.KeyType: execwire.TypeNotice, "notice": event.Notice})
	case events.EventSystemPrompt:
		return writeJSONLine(out, map[string]any{execwire.KeyType: execwire.TypeSystemPrompt, execwire.KeySystem: event.System})
	case events.EventReasoning:
		return writeJSONLine(out, map[string]any{execwire.KeyType: execwire.TypeReasoning, execwire.KeyReasoning: event.Reasoning})
	default:
		return nil
	}
}

// execProgress records the tool calls a run completed. EventAfterResponse — the
// only thing that writes a transcript — never fires when the process is killed
// mid-run, so without this a SIGTERM leaves no session to resume at all.
type execProgress struct {
	mu    sync.Mutex
	tools []string
}

func (p *execProgress) register(bus events.Bus, sessionID string) func() {
	if bus == nil {
		return func() {}
	}
	return bus.Register(events.HandlerFunc(func(_ context.Context, event events.Event) error {
		if event.SessionID != sessionID || event.Type != events.EventToolCompleted || event.Tool == nil {
			return nil
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		p.tools = append(p.tools, event.Tool.Name)
		return nil
	}))
}

// note is the assistant line stored for a cut-short turn: the ledger of what
// actually ran, so the resume plans from facts instead of guessing.
func (p *execProgress) note() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.tools) == 0 {
		return "[The run was interrupted before any tool call completed.]"
	}
	return fmt.Sprintf("[The run was interrupted before it finished. Completed tool calls (%d): %s. The work is unfinished — check the workspace state before continuing.]",
		len(p.tools), strings.Join(p.tools, ", "))
}

// persistInterrupted writes the partial transcript when the run was cancelled
// (SIGINT/SIGTERM propagates through the root context). A plain provider error
// is not an interrupt and keeps the old behavior of storing nothing.
func (p *execProgress) persistInterrupted(ctx context.Context, s runtime.Services, sessionID string, prompt string) {
	if ctx.Err() == nil {
		return
	}
	// The cancelled context cannot carry the write it exists to record.
	writeCtx := context.WithoutCancel(ctx)
	store := sessionstore.NewForWorkspace(s.App.Memory.Dir, s.App.Workspace.Root, s.Logger)
	if _, err := store.Append(writeCtx, sessionID, "user", prompt); err != nil {
		logExecWarn(s.Logger, "session store: could not persist the interrupted prompt", err)
		return
	}
	if _, err := store.AppendInterrupted(writeCtx, sessionID, "assistant", p.note(), sessionstore.ToolRecord{}); err != nil {
		logExecWarn(s.Logger, "session store: could not persist the interrupt marker", err)
	}
}

func logExecWarn(logger *zap.Logger, message string, err error) {
	if logger != nil {
		logger.Warn(message, zap.Error(err))
	}
}

type execApprovalPolicy struct {
	allowAll bool
	tiers    map[string]bool
}

func newExecApprovalPolicy(opts execOptions) execApprovalPolicy {
	policy := execApprovalPolicy{allowAll: opts.allowAll, tiers: make(map[string]bool)}
	for _, tier := range opts.allowTiers {
		tier = strings.TrimSpace(tier)
		if tier == "read-only" {
			policy.tiers[constants.ShellTierReadonly] = true
			policy.tiers[constants.CodeInterpreterTierReadWorkspace] = true
		} else if tier != "" {
			policy.tiers[tier] = true
		}
	}
	return policy
}

func (p execApprovalPolicy) approves(req approval.Request) bool {
	if p.allowAll {
		return true
	}
	if strings.TrimSpace(req.Tier) == "" {
		return false
	}
	return !slices.ContainsFunc(strings.Split(req.Tier, ","), func(tier string) bool { return !p.tiers[strings.TrimSpace(tier)] })
}

// serveExecApprovals answers approval requests non-interactively. Standing
// rules are consulted first: a grant or denial the user already made applies
// here just as it does in the TUI, otherwise it would silently do nothing
// outside it. The default without a matching rule stays deny-all.
func serveExecApprovals(broker *approval.Broker, grants *approval.Store, policy execApprovalPolicy) func() {
	return approval.Drain(broker, func(req approval.Request) approval.Decision {
		if req.PolicyChecked && broker.Policy != nil && broker.Policy.Mode() != approval.Manual {
			return approval.Decision{Reason: "operation requires interactive approval in " + string(broker.Policy.Mode()) + " mode"}
		}
		if decision, ok := grants.Decide(req); ok {
			return decision
		}
		approved := policy.approves(req)
		reason := approval.DenyReason(req)
		if approved {
			reason = "approved by exec policy"
		}
		return approval.Decision{Approved: approved, Reason: reason}
	})
}

// validateExecOutput enforces the --output-schema contract. The schema file is
// injected into the prompt (see runExec) to steer the model; enforcement here
// is deliberately JSON-validity only, matching the flag's documented behavior
// ("Path to a JSON Schema: it is appended to the prompt, and the run fails unless the final response parses as JSON"). We intentionally avoid
// pulling in a full JSON Schema validator dependency.
func validateExecOutput(message string, schemaPath string) error {
	if schemaPath == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(message), &value); err != nil {
		return fmt.Errorf("final response is not valid JSON: %w", err)
	}
	return nil
}

// writeJSONLine stamps every emitted ledger line with "ts" (RFC3339Nano) so
// exec --json consumers can measure wall time between events, then encodes it.
func writeJSONLine(out io.Writer, value map[string]any) error {
	value[execwire.KeyTs] = time.Now().UTC().Format(time.RFC3339Nano)
	return json.NewEncoder(out).Encode(value)
}
