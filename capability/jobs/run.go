package jobs

import (
	"context"
	"fmt"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/platform/turn"
	"go.uber.org/zap"
)

// RunOptions tunes how Run records and reports a job run.
type RunOptions struct {
	// FailOnToolError records an error status and returns an error when the
	// run completed but had tool failures.
	FailOnToolError bool
	// Logger reports RecordRun bookkeeping failures; nil silences them.
	Logger *zap.Logger
}

// Run executes job's prompt on the caller-supplied session, auto-deciding
// approval requests from broker per the job's approval policy, and records the
// run status in store. The session is injected by the caller so this package
// stays free of runtime dependencies (platform/runtime imports jobs, not the
// other way around).
func Run(ctx context.Context, session turn.Session, broker *approval.Broker, store *Store, job Job, opts RunOptions) error {
	stop := drainApprovals(broker, job.ApprovalPolicy == "allow")
	defer stop()

	resp, err := session.Send(ctx, job.Prompt)
	if err != nil {
		RecordStatus(store, job.ID, "error: "+err.Error(), opts.Logger)
		return err
	}
	if opts.FailOnToolError && len(resp.ToolFailures) > 0 {
		status := fmt.Sprintf("error: %d tool failure(s)", len(resp.ToolFailures))
		RecordStatus(store, job.ID, status, opts.Logger)
		return fmt.Errorf("jobs: %s: %s", job.Name, status)
	}
	RecordStatus(store, job.ID, "ok", opts.Logger)
	return nil
}

// RecordStatus persists a run status for jobID, truncating it to 255 runes.
// It uses a background context so a cancelled run still gets its status
// recorded; RecordRun failures are logged on logger when non-nil.
func RecordStatus(store *Store, jobID, status string, logger *zap.Logger) {
	if runes := []rune(status); len(runes) > 255 {
		status = string(runes[:255])
	}
	if err := store.RecordRun(context.Background(), jobID, status); err != nil && logger != nil {
		logger.Warn("record run failed", zap.Error(err))
	}
}

// drainApprovals drains the approval broker's request channel, deciding each
// pending request according to the approved flag. It returns a stop function
// that cancels the goroutine.
func drainApprovals(broker *approval.Broker, approved bool) func() {
	reason := "job policy deny"
	if approved {
		reason = "job policy allow"
	}
	return approval.Drain(broker, func(approval.Request) approval.Decision {
		return approval.Decision{Approved: approved, Reason: reason}
	})
}
