package clients

import (
	"context"
	"errors"
	"time"

	inference "github.com/rivt-ai/go-inference-router"
)

// retryBaseDelay is the first backoff step; each further attempt doubles it.
// A throttled provider's own Retry-After wins over this schedule.
const retryBaseDelay = time.Second

// retryVeto hides the typed provider error from the retry policy's Kind
// classification, so a failure that is no longer safe to re-issue (streamed
// output already reached the user) stops the policy immediately.
type retryVeto struct{ err error }

func (v retryVeto) Error() string { return v.err.Error() }

// retry re-issues call through the router's RetryPolicy while the driver
// reports a retryable failure (rate limits, transport faults), up to
// MaxRetries extra attempts. Stalls are excluded: the provider accepted the
// request and went quiet, so re-sending can pay for a second generation.
// A non-nil safeToRetry vetoes further attempts once re-issuing would no
// longer be side-effect free.
func (c *Client) retry(ctx context.Context, safeToRetry func() bool, call func(context.Context) (*inference.Response, error)) (*inference.Response, error) {
	var resp *inference.Response
	policy := inference.RetryPolicy{MaxAttempts: c.MaxRetries + 1, BaseDelay: retryBaseDelay}
	err := policy.Do(ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = call(ctx)
		if callErr != nil && safeToRetry != nil && !safeToRetry() {
			return retryVeto{callErr}
		}
		return callErr
	})
	if err != nil {
		var veto retryVeto
		if errors.As(err, &veto) {
			err = veto.err
		}
		return nil, normalizeProviderError(err)
	}
	return resp, nil
}
