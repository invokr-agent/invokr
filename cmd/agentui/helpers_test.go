package agentui

import (
	"context"
	"io"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
)

func newModel(ctx context.Context, session turn.Session, toolEvents *toolEventQueue, approvalQueue *approval.ApprovalQueue, logs LogSource, logView bool, promptHistory turn.PromptHistory, initialHistory []string) *model {
	return newModelWithCommands(ctx, session, nil, toolEvents, approvalQueue, logs, logView, promptHistory, initialHistory)
}

func runPlain(ctx context.Context, session turn.Session, eventBus events.Bus, in io.Reader, out io.Writer, errOut io.Writer) error {
	return runPlainWithCommands(ctx, session, nil, nil, nil, eventBus, in, out, errOut)
}

func runPlainWithApprovals(ctx context.Context, session turn.Session, approvals *approval.Broker, eventBus events.Bus, in io.Reader, out io.Writer, errOut io.Writer) error {
	return runPlainWithCommands(ctx, session, nil, approvals, nil, eventBus, in, out, errOut)
}

func handlePlainLocalCommand(out io.Writer, session turn.Session, text string) (handled bool, exit bool) {
	return handlePlainLocalCommandWithBackend(context.Background(), out, session, nil, text)
}
