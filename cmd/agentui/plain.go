package agentui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"
)

func runPlainWithCommands(ctx context.Context, session turn.Session, commands CommandBackend, approvals *approval.Broker, grants *approval.Store, eventBus events.Bus, in io.Reader, out io.Writer, errOut io.Writer) error {
	// Nothing can answer an approval prompt when stdin is a pipe, so a request
	// is approved only by a standing grant and otherwise auto-denied with a
	// notice. Left undrained the broker blocks the turn forever.
	defer denyPlainApprovals(approvals, grants, errOut)()

	// Tool-progress events go to stderr so stdout carries only the assistant
	// response, matching the exec adapter and keeping the two streams separable.
	renderer := plainResponseRenderer{out: out, errOut: errOut}
	unregister := registerToolEvents(eventBus, session, renderer.handleEvent, nil)
	defer unregister()
	defer context.AfterFunc(ctx, func() {
		if input, ok := in.(io.Closer); ok {
			_ = input.Close()
		}
	})()

	writePlain(out, renderIntro(0, 0), "\n", renderPrompt())

	scanner := bufio.NewScanner(in)
	scanned := make(chan bool, 1)
	for {
		go func() { scanned <- scanner.Scan() }()
		var ok bool
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ok = <-scanned:
		}
		if !ok {
			return scanner.Err()
		}
		text := strings.TrimSpace(scanner.Text())

		if text == "" {
			writePlain(out, renderPrompt())
			continue
		}

		handled, exit := handlePlainLocalCommandWithBackend(ctx, out, session, commands, text)
		if exit {
			return nil
		}
		if handled {
			continue
		}

		renderer.deltaRendered = false
		resp, err := session.Send(ctx, text)
		if err != nil {
			return err
		}
		renderer.finish(resp)
	}
}

// plainDenyReason is the decision reason recorded in the outcome ledger for an
// approval raised while stdin is not a terminal. It names the policy and the
// remedy so the model reports a policy block rather than a missing capability.
func plainDenyReason(req approval.Request) string {
	return "no terminal to approve on; " + approval.DenyReason(req)
}

func denyPlainApprovals(approvals *approval.Broker, grants *approval.Store, errOut io.Writer) func() {
	return approval.Drain(approvals, func(req approval.Request) approval.Decision {
		if req.PolicyChecked && approvals.Policy.Mode() != approval.Manual {
			return approval.Decision{Reason: plainDenyReason(req)}
		}
		decision, ok := grants.Decide(req)
		if !ok {
			decision = approval.Decision{Reason: plainDenyReason(req)}
		}
		if !decision.Approved {
			writePlain(errOut, "✕ ", req.Tool, " auto-denied: ", decision.Reason, "\n")
		}
		return decision
	})
}

type plainResponseRenderer struct {
	out, errOut   io.Writer
	deltaRendered bool
}

func (r *plainResponseRenderer) handleEvent(ev events.Event) {
	if ev.Type != events.EventAssistantDelta {
		writePlain(r.errOut, renderToolEvent(ev), "\n")
		return
	}
	if !r.deltaRendered {
		writePlain(r.out, renderResponseHeader(0), "\n")
		r.deltaRendered = true
	}
	writePlain(r.out, ev.Delta.Text)
}

func (r *plainResponseRenderer) finish(resp turn.AgentResponse) {
	if !r.deltaRendered {
		message := turn.AppendExecutionGuard(turn.AppendToolOutcomeGuard(resp.Message, resp.ToolFailures), resp.ExecutionEvidence)
		writePlain(r.out, renderResponseWithUsage(message, resp.Usage, 0), "\n\n", renderPrompt())
		return
	}
	if guard := turn.AppendExecutionGuard(turn.AppendToolOutcomeGuard("", resp.ToolFailures), resp.ExecutionEvidence); guard != "" {
		writePlain(r.out, "\n\n", guard)
	}
	writePlain(r.out, renderPlainUsage(resp.Usage, 0), "\n\n", renderPrompt())
}

func handlePlainLocalCommandWithBackend(ctx context.Context, out io.Writer, session turn.Session, commands CommandBackend, text string) (handled bool, exit bool) {
	switch text {
	case "exit", "quit":
		return true, true
	case "help":
		writePlain(out, plainCommandHelp(), "\n\n", renderPrompt())
		return true, false
	default:
		if result, handled := plainCommand(ctx, commands, session, text); handled {
			writePlain(out, result, "\n\n", renderPrompt())
			return true, false
		}
		return false, false
	}
}

func listModelsPlain(out io.Writer, session turn.Session) {
	current := currentModel(session)
	lister, ok := session.(modelLister)
	if !ok {
		writePlain(out, "current model: "+current+"\n(provider does not support model listing)")
		return
	}
	models, err := lister.ListModels(context.Background())
	if err != nil {
		writePlain(out, "error fetching models: "+err.Error())
		return
	}
	if len(models) == 0 {
		writePlain(out, "current model: "+current+"\n(provider returned no model list)")
		return
	}
	writePlain(out, renderModelList(models, current)+"Use /model <name> to switch.")
}

func renderModelList(models []string, current string) string {
	var b strings.Builder
	for _, id := range models {
		if id == current {
			b.WriteString("  * " + id + "  ← current\n")
		} else {
			b.WriteString("    " + id + "\n")
		}
	}
	return b.String()
}

// plainWriteMu serialises plain-mode writes: tool/delta events arrive on the
// event-bus goroutines while the read loop writes prompts and responses, so
// unguarded Fprint calls interleave mid-line.
var plainWriteMu sync.Mutex

func writePlain(w io.Writer, args ...any) {
	plainWriteMu.Lock()
	defer plainWriteMu.Unlock()
	//nolint:errcheck
	fmt.Fprint(w, args...)
}
