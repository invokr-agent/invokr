package acpagent

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/invokr-agent/acp"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/runtime"
	"github.com/invokr-agent/invokr/platform/turn"
	"github.com/invokr-agent/invokr/platform/version"
)

var permissionOptions = []acp.PermissionOption{
	{OptionID: "allow", Name: "Allow", Kind: acp.PermissionOptionKindAllowOnce},
	{OptionID: "deny", Name: "Deny", Kind: acp.PermissionOptionKindRejectOnce},
}

type chatSession struct {
	turn.IdentifiedSession
	approvals *approval.Broker
	turnMu    sync.Mutex
	noticed   bool     // the update notice was sent in this session; guarded by turnMu
	models    []string // listed at session/new; read-only after
}

type Commands interface {
	Available() []acp.AvailableCommand
	// Run returns prompt instead of reply when the command becomes a model turn.
	Run(ctx context.Context, sess turn.Session, text string) (reply, prompt string, handled bool)
}

// Agent serves invokr chat sessions over ACP; sessions live only as long as the process.
type Agent struct {
	conn       *acp.AgentConn
	newSession runtime.ChatSessionFunc
	commands   Commands
	sessions   sync.Map
	// bound is the host's own session: a shared TUI serves its chat contacts
	// from it, so turns land in the transcript, mode and approvals the human sees.
	bound *chatSession
}

// latestVersion is the newer invokr release tag, "" when none is known; tests replace it.
var latestVersion = version.LatestVersion

func Serve(ctx context.Context, in io.Reader, out io.Writer, bus events.Bus, newSession runtime.ChatSessionFunc, commands Commands) error {
	return serve(ctx, &Agent{conn: acp.NewAgentConn(in, out), newSession: newSession, commands: commands}, bus)
}

func serve(ctx context.Context, a *Agent, bus events.Bus) error {
	stopUsage := a.registerUsage(bus)
	defer stopUsage()
	latestVersion() // starts the background lookup so the first reply can carry its answer
	a.conn.Serve(ctx, a)
	<-a.conn.Done()
	return a.conn.Err()
}

// registerUsage forwards the runner's running usage to the client, so the editor
// meters the context window while a prompt is still running. One handler serves
// the process; publishUsage decides which sessions are this agent's.
func (a *Agent) registerUsage(bus events.Bus) func() {
	if bus == nil {
		return func() {}
	}
	return bus.Register(events.HandlerFunc(func(ctx context.Context, ev events.Event) error {
		a.publishUsage(ctx, ev)
		return nil
	}))
}

// publishUsage sends the running context as an ACP usage_update. No Cost: invokr's
// provider.pricing is currency-free and ACP Cost requires an ISO currency code.
func (a *Agent) publishUsage(ctx context.Context, ev events.Event) {
	if ev.Type != events.EventUsage || ev.Usage == nil {
		return
	}
	id := acp.SessionID(ev.SessionID)
	if _, served := a.sessions.Load(id); !served {
		return
	}
	usage := *ev.Usage
	if !usage.Available || usage.ContextWindowTokens <= 0 {
		return
	}
	used := usage.LatestPromptTokens + usage.LatestCompletionTokens
	_ = a.update(context.WithoutCancel(ctx), id, acp.SessionUpdate{UsageUpdate: &acp.UsageUpdate{
		Used: uint64(used), Size: uint64(usage.ContextWindowTokens)}})
}

func (a *Agent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	text := &acp.AgentCapabilities{PromptCapabilities: &acp.PromptCapabilities{}}
	return acp.InitializeResponse{ProtocolVersion: acp.V1, AgentCapabilities: text, AgentInfo: &acp.Implementation{Name: "invokr", Version: version.Version}}, nil
}

func (a *Agent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

// NewSession only serves the workspace invokr was started in: sessions cannot carry their own.
func (a *Agent) NewSession(ctx context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	wd, err := os.Getwd()
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	if !sameDir(req.Cwd, wd) {
		return acp.NewSessionResponse{}, fmt.Errorf("acp: cwd %q differs from the workspace invokr was started in (%q); start invokr acp in that directory", req.Cwd, wd)
	}
	// One shared TUI has one session: a contact gets the host's, not a fresh one,
	// and asking again must not list its models and commands a second time.
	var (
		store = true
		chat  *chatSession
		id    acp.SessionID
	)
	if a.bound != nil {
		chat = a.bound
		id = acp.SessionID(chat.SessionID())
		_, already := a.sessions.Load(id)
		store = !already
	} else {
		sess, broker, err := a.newSession("")
		if err != nil {
			return acp.NewSessionResponse{}, err
		}
		chat = &chatSession{IdentifiedSession: sess, approvals: broker}
		id = acp.SessionID(sess.SessionID())
	}
	if store {
		a.sessions.Store(id, chat)
		listModels(ctx, chat)
		commands := a.available()
		go func() {
			_ = a.update(context.WithoutCancel(ctx), id, acp.SessionUpdate{AvailableCommandsUpdate: &acp.AvailableCommandsUpdate{
				AvailableCommands: commands}})
		}()
	}
	return acp.NewSessionResponse{SessionID: id, Modes: modeState(chat.approvals.Policy), ConfigOptions: configOptions(chat)}, nil
}

func (a *Agent) session(id acp.SessionID) (*chatSession, error) {
	v, ok := a.sessions.Load(id)
	if !ok {
		return nil, acp.NewError(acp.ErrInvalidParams, fmt.Sprintf("acp: unknown session %q", id))
	}
	return v.(*chatSession), nil
}

func (a *Agent) update(ctx context.Context, id acp.SessionID, u acp.SessionUpdate) error {
	return a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionID: id, Update: u})
}

func sameDir(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

func (a *Agent) Prompt(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	sess, err := a.session(req.SessionID)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	var text []string
	for _, block := range req.Prompt {
		if block.Text == nil {
			return acp.PromptResponse{}, acp.NewError(acp.ErrInvalidParams, "acp: only text prompt blocks are supported")
		}
		text = append(text, block.Text.Text)
	}
	sess.turnMu.Lock()
	defer sess.turnMu.Unlock()
	input := strings.Join(text, "\n")
	// Pin before commands run: a skill, tool command or job a chat turn starts
	// inherits this context, and an unwrapped one is the host's to answer.
	ctx = a.pinned(ctx)
	if reply, ok := a.modeText(ctx, req.SessionID, sess, input); ok {
		return a.reply(ctx, req.SessionID, reply)
	}
	if a.bound != nil && spawnsDetachedSession(input) {
		return a.reply(ctx, req.SessionID, "a shared session cannot start this command: it runs detached, with its own approvals")
	}
	if a.commands != nil {
		if reply, prompt, ok := a.commands.Run(ctx, sess.IdentifiedSession, input); ok {
			if prompt == "" {
				// /model, /reasoning and /profile change options; the update is cheap for the rest.
				_ = a.update(ctx, req.SessionID, acp.SessionUpdate{ConfigOptionUpdate: &acp.ConfigOptionUpdate{ConfigOptions: configOptions(sess)}})
				return a.reply(ctx, req.SessionID, reply)
			}
			input = prompt
		}
	}
	return a.send(ctx, req.SessionID, sess, input)
}

// send runs one model turn; the caller holds sess.turnMu.
func (a *Agent) send(ctx context.Context, id acp.SessionID, sess *chatSession, input string) (acp.PromptResponse, error) {
	var denied []turn.ToolOutcome
	var approved []acp.ToolCallID
	// A bound session's approvals go to the host's queue, which mirrors them
	// here; draining the broker too would split each request between consumers.
	stopDrain := func() {}
	if a.bound == nil {
		stopDrain = approval.Drain(sess.approvals, func(r approval.Request) approval.Decision {
			d, _ := a.ask(ctx, id, r)
			if d.Approved {
				approved = append(approved, acp.ToolCallID(r.ID))
			} else {
				denied = append(denied, turn.ToolOutcome{Tool: r.Tool, Detail: strings.TrimPrefix(cmp.Or(r.Summary, r.Tool), "Run ")})
			}
			return d
		})
	}
	resp, err := sess.Send(a.pinned(ctx), input)
	stopDrain()
	final := acp.ToolCallStatusCompleted
	if err != nil {
		final = acp.ToolCallStatusFailed
	}
	for _, callID := range approved {
		update := acp.SessionUpdate{ToolCallUpdate: &acp.ToolCallUpdate{ToolCallID: callID, Status: final}}
		_ = a.conn.SessionUpdate(context.WithoutCancel(ctx), acp.SessionNotification{SessionID: id, Update: update})
	}
	if err != nil {
		if ctx.Err() != nil {
			return acp.PromptResponse{}, ctx.Err()
		}
		slog.Error("acp: prompt failed", "error", err)
		return acp.PromptResponse{}, acp.NewError(acp.ErrInternalError, "model request failed")
	}
	reply := strings.TrimSpace(resp.Message) + toolTrailer(resp.ExecutionEvidence, resp.ToolFailures, denied)
	if tag := latestVersion(); tag != "" && !sess.noticed {
		sess.noticed = true
		reply += fmt.Sprintf("\n\nℹ️ invokr %s is available (running %s): run `invokr update`", tag, version.Version)
	}
	return a.reply(ctx, id, reply)
}

func (a *Agent) reply(ctx context.Context, id acp.SessionID, text string) (acp.PromptResponse, error) {
	chunk := acp.SessionUpdate{AgentMessageChunk: &acp.ContentChunk{Content: acp.ContentBlock{Text: &acp.TextContent{Text: text}}}}
	if err := a.update(ctx, id, chunk); err != nil {
		return acp.PromptResponse{}, err
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func toolTrailer(evidence []turn.ExecutionEvidence, failures, denied []turn.ToolOutcome) string {
	pending := slices.Clone(failures)
	labels := slices.Clone(denied)
	var items []string
	for _, e := range evidence {
		label, mark := evidenceMark(e, &pending, &labels)
		items = append(items, label+" "+mark)
	}
	for _, f := range pending {
		items = append(items, deniedLabel(f, &labels)+" ✗ "+f.Kind)
	}
	if len(items) == 0 {
		return ""
	}
	return "\n\n— tools: " + strings.Join(items, "; ")
}

func takeFirst(list *[]turn.ToolOutcome, tool string) (turn.ToolOutcome, bool) {
	i := slices.IndexFunc(*list, func(o turn.ToolOutcome) bool { return o.Tool == tool })
	if i < 0 {
		return turn.ToolOutcome{}, false
	}
	o := (*list)[i]
	*list = slices.Delete(*list, i, i+1)
	return o, true
}

func deniedLabel(f turn.ToolOutcome, labels *[]turn.ToolOutcome) string {
	if f.Kind == turn.OutcomeKindDenied {
		if l, ok := takeFirst(labels, f.Tool); ok {
			return l.Detail
		}
	}
	return cmp.Or(f.Tool, "unknown")
}

func evidenceMark(e turn.ExecutionEvidence, pending, labels *[]turn.ToolOutcome) (string, string) {
	label := cmp.Or(e.Command, e.Tool)
	if e.Outcome == "completed" && (e.ExitCode == nil || *e.ExitCode == 0) {
		return label, "✓"
	}
	outcome := e.Outcome
	if f, ok := takeFirst(pending, e.Tool); ok {
		if outcome != "flagged" {
			outcome = f.Kind
		}
		if e.Command == "" {
			label = deniedLabel(f, labels)
		}
	}
	if e.ExitCode != nil {
		return label, fmt.Sprintf("✗ %s exit=%d", outcome, *e.ExitCode)
	}
	return label, "✗ " + outcome
}

func (a *Agent) available() []acp.AvailableCommand {
	commands := []acp.AvailableCommand{modeCommand}
	if a.commands != nil {
		commands = append(commands, a.commands.Available()...)
	}
	return commands
}

func (a *Agent) pinned(ctx context.Context) context.Context {
	if a.bound == nil {
		return ctx
	}
	return approval.Pin(ctx)
}

// mayAsk is the whole trust boundary of a shared connection: only a chat-started
// (pinned) turn's calls may be put to the relay. A host turn's approval shown
// in chat is answered by nobody who decided it, and the relay edits its own
// prompt to "Approved" when a contact taps a stale button.
func (a *Agent) mayAsk(req approval.Request) bool { return a.bound == nil || req.Pinned }

// ask shows the call to the chat contact. The flag says the chat answered;
// a dead connection is not a denial, and must not settle the host's request.
func (a *Agent) ask(ctx context.Context, id acp.SessionID, req approval.Request) (approval.Decision, bool) {
	if !a.mayAsk(req) {
		return approval.Decision{Reason: "the host's own approval belongs to the terminal"}, false
	}
	callID := acp.ToolCallID(req.ID)
	title, kind := cmp.Or(req.Summary, req.Tool), toolKind(req)
	announce := acp.SessionUpdate{ToolCall: &acp.ToolCall{ToolCallID: callID, Name: req.Tool, Title: title, Kind: kind, Status: acp.ToolCallStatusPending}}
	if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionID: id, Update: announce}); err != nil {
		return approval.Decision{Reason: "permission request failed"}, false
	}
	resp, err := a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{SessionID: id, Options: permissionOptions,
		ToolCall: acp.ToolCallUpdate{ToolCallID: callID, Name: req.Tool, Title: title, Kind: kind}})
	decision, status := approval.Decision{Reason: "denied by user"}, acp.ToolCallStatusFailed
	if err == nil && resp.Outcome.Selected != nil && resp.Outcome.Selected.OptionID == permissionOptions[0].OptionID {
		decision, status = approval.Decision{Approved: true}, acp.ToolCallStatusInProgress
	}
	update := acp.SessionUpdate{ToolCallUpdate: &acp.ToolCallUpdate{ToolCallID: callID, Status: status}}
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionID: id, Update: update})
	return decision, err == nil
}

func toolKind(req approval.Request) acp.ToolKind {
	tiers := strings.Split(req.Tier, ",")
	switch {
	case req.Tool == constants.ShellToolName || slices.Contains(tiers, constants.CodeInterpreterTierCodeExecution):
		return acp.ToolKindExecute
	case req.Writes || slices.Contains(tiers, "write_workspace") || slices.Contains(tiers, constants.CodeInterpreterTierArtifactWrite):
		return acp.ToolKindEdit
	case req.Network || slices.Contains(tiers, constants.CodeInterpreterTierNetworkCommand):
		return acp.ToolKindFetch
	case req.ReadOnly || slices.Contains(tiers, constants.CodeInterpreterTierReadWorkspace):
		return acp.ToolKindRead
	}
	return acp.ToolKindOther
}

func (a *Agent) Cancel(context.Context, acp.CancelNotification) error { return nil }

// detachedSessions are slash commands whose work outlives the turn that asked:
// a scheduled job runs on its own context with a fresh session and approvals,
// so the pin cannot carry into it.
var detachedSessions = []string{"/jobs"}

func spawnsDetachedSession(input string) bool {
	fields := strings.Fields(input)
	return len(fields) > 0 && slices.Contains(detachedSessions, strings.ToLower(fields[0]))
}
