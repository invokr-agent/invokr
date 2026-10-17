// Package jobrun executes scheduled jobs via a fresh runtime bootstrap. Split
// from internal/jobs because internal/runtime imports tools/system, which
// needs to import internal/jobs (schedule tool) without a cycle.
package jobrun

import (
	"context"
	"fmt"

	"github.com/invokr-ai/invokr/capability/jobs"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/runtime"
	"go.uber.org/zap"
)

// RunJob executes a scheduled job using a fresh runtime bootstrap. The run
// itself (approval drain, send, status recording) is jobs.Run; this wrapper
// only supplies the session.
func RunJob(ctx context.Context, app config.AppConfig, store *jobs.Store, job jobs.Job, logger *zap.Logger, failOnToolError bool) error {
	rt, err := runtime.Bootstrap(app, runtime.Options{})
	if err != nil {
		jobs.RecordStatus(store, job.ID, "error: bootstrap: "+err.Error(), logger)
		return fmt.Errorf("jobs: bootstrap %s: %w", job.Name, err)
	}
	// Teardown order is now stop-drain (inside jobs.Run) then rt.Cleanup(),
	// the reverse of the previous single deferred block. Safe: runtime
	// cleanup issues no approval requests, so nothing can block on the
	// already-stopped drain goroutine.
	defer func() { _ = rt.Cleanup() }()

	s := rt.Services()
	return jobs.Run(ctx, s.Session, s.Approvals, store, job,
		jobs.RunOptions{FailOnToolError: failOnToolError, Logger: logger})
}
