//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/testutil/providertest"
)

func TestR01_ProviderUnreachable(t *testing.T) {
	const url = "http://127.0.0.1:1"
	home := newHome(t, url)
	result := runExec(t, home, url, "fail cleanly")
	if result.Err == nil || result.TimedOut || strings.Contains(result.Stderr, "panic:") {
		t.Fatalf("unexpected result: exit=%d timeout=%v err=%v stderr=%s", result.ExitCode, result.TimedOut, result.Err, result.Stderr)
	}
}

func TestR02a_TransientHTTP500Retries(t *testing.T) {
	provider := providertest.NewScripted(t,
		providertest.ErrorStep(http.StatusInternalServerError, providerError("server_error", "boom")),
		providertest.ReplyStep("recovered"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	result := runExec(t, home, provider.URL(), "retry")
	if result.Err != nil {
		t.Fatalf("exec failed: %v %s", result.Err, result.Stderr)
	}
	if got := len(provider.Snapshot().CompletionRequests); got != 2 {
		t.Fatalf("completion requests = %d, want 2", got)
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

func TestR02b_RetryAfterHonored(t *testing.T) {
	provider := providertest.NewScripted(t,
		providertest.CompletionStep{Status: http.StatusTooManyRequests, Body: providerError("rate_limit_error", "slow down"), RetryAfterSeconds: 1},
		providertest.ReplyStep("recovered"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	result := runExec(t, home, provider.URL(), "retry later")
	if result.Err != nil {
		t.Fatalf("exec failed: %v %s", result.Err, result.Stderr)
	}
	requests := provider.Snapshot().CompletionRequests
	if len(requests) != 2 || requests[1].ReceivedAt.Sub(requests[0].ReceivedAt) < time.Second {
		t.Fatalf("request timings = %#v", requests)
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

func TestR02c_PersistentFailureAndStallBounded(t *testing.T) {
	t.Run("persistent 500", func(t *testing.T) {
		steps := make([]providertest.CompletionStep, 3)
		for i := range steps {
			steps[i] = providertest.ErrorStep(http.StatusInternalServerError, providerError("server_error", "still down"))
		}
		provider := providertest.New(t, steps)
		defer provider.Close()
		home := newHome(t, provider.URL())
		result := runExec(t, home, provider.URL(), "fail after retries")
		if result.Err == nil || result.TimedOut {
			t.Fatalf("unexpected result: timeout=%v err=%v", result.TimedOut, result.Err)
		}
		provider.AssertClean(t)
		provider.AssertExhausted(t)
	})

	t.Run("watchdog", func(t *testing.T) {
		provider := providertest.NewScripted(t, providertest.CompletionStep{StallDuration: time.Minute})
		defer provider.Close()
		home := newHome(t, provider.URL())
		rewriteConfig(t, &home, func(app *config.AppConfig) {
			app.Provider.StallTimeout = 100 * time.Millisecond
			app.Provider.HTTPTimeout = 2 * time.Second
		})
		result := runExec(t, home, provider.URL(), "watchdog")
		if result.Err == nil || result.TimedOut || !strings.Contains(result.Stderr, "provider stalled") {
			t.Fatalf("unexpected result: timeout=%v err=%v stderr=%s", result.TimedOut, result.Err, result.Stderr)
		}
		provider.AssertClean(t)
		provider.AssertExhausted(t)
	})
}

func TestR03a_MidstreamDropFailsWithoutHang(t *testing.T) {
	provider := providertest.NewScripted(t,
		providertest.CompletionStep{SSEChunks: []providertest.CompletionStreamChunk{{Content: `{"type":"reply"`}}, DropAfterChunks: 1},
		providertest.ReplyStep("must remain unused"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	result := runExec(t, home, provider.URL(), "drop stream")
	if result.Err == nil || result.ExitCode == 0 || result.TimedOut || len(provider.Snapshot().CompletionRequests) != 1 ||
		!strings.Contains(result.Stderr, "unexpected EOF") || strings.Contains(result.Stderr, "panic:") {
		t.Fatalf("unexpected result: exit=%d timeout=%v err=%v requests=%d stderr=%q", result.ExitCode, result.TimedOut,
			result.Err, len(provider.Snapshot().CompletionRequests), result.Stderr)
	}
	provider.AssertClean(t)
}

func TestR03b_PersistentMidstreamDropBounded(t *testing.T) {
	provider := providertest.NewScripted(t, providertest.CompletionStep{
		SSEChunks:       []providertest.CompletionStreamChunk{{Content: "partial"}},
		DropAfterChunks: 1,
	})
	defer provider.Close()
	home := newHome(t, provider.URL())
	result := runExec(t, home, provider.URL(), "drop stream")
	if result.Err == nil || result.ExitCode == 0 || result.TimedOut || len(provider.Snapshot().CompletionRequests) != 1 ||
		!strings.Contains(result.Stderr, "unexpected EOF") || strings.Contains(result.Stderr, "panic:") {
		t.Fatalf("unexpected result: exit=%d timeout=%v err=%v requests=%d stderr=%q", result.ExitCode, result.TimedOut,
			result.Err, len(provider.Snapshot().CompletionRequests), result.Stderr)
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

func TestR04_MalformedBodyFails(t *testing.T) {
	provider := providertest.NewScripted(t, providertest.CompletionStep{MalformedBody: "{not json"})
	defer provider.Close()
	home := newHome(t, provider.URL())
	result := runExec(t, home, provider.URL(), "malformed")
	if result.Err == nil || result.TimedOut || !strings.Contains(result.Stderr, "without any completion chunks") {
		t.Fatalf("unexpected result: timeout=%v err=%v stderr=%s", result.TimedOut, result.Err, result.Stderr)
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

func TestR05_HTTP400NotRetried(t *testing.T) {
	provider := providertest.NewScripted(t, providertest.ErrorStep(http.StatusBadRequest, providerError("invalid_request_error", "unknown parameter: foo")))
	defer provider.Close()
	home := newHome(t, provider.URL())
	result := runExec(t, home, provider.URL(), "bad request")
	if result.Err == nil || result.TimedOut || !strings.Contains(result.Stderr, "unknown parameter") || len(provider.Snapshot().CompletionRequests) != 1 {
		t.Fatalf("unexpected result: timeout=%v err=%v stderr=%s", result.TimedOut, result.Err, result.Stderr)
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

// TestR05b_ContextOverflowRetriedOnce covers the one 400 the runner does retry
// (#313): a context-length rejection forces compaction and resends exactly once.
func TestR05b_ContextOverflowRetriedOnce(t *testing.T) {
	provider := providertest.NewScripted(t,
		providertest.ErrorStep(http.StatusBadRequest, providerError("invalid_request_error", "context length exceeded")),
		providertest.ErrorStep(http.StatusBadRequest, providerError("invalid_request_error", "context length exceeded")),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	result := runExec(t, home, provider.URL(), "too large")
	if result.Err == nil || result.TimedOut || !strings.Contains(result.Stderr, "context length exceeded") || len(provider.Snapshot().CompletionRequests) != 2 {
		t.Fatalf("unexpected result: timeout=%v err=%v requests=%d stderr=%s", result.TimedOut, result.Err, len(provider.Snapshot().CompletionRequests), result.Stderr)
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

func providerError(kind, message string) string {
	return "{\"error\":{\"type\":\"" + kind + "\",\"message\":\"" + message + "\"}}"
}
