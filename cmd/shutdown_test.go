package cmd

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	appruntime "github.com/invokr-agent/invokr/platform/runtime"
)

// stubShutdownSpinner replaces the spinner for the duration of a test and
// reports whether the progress path was taken.
func stubShutdownSpinner(t *testing.T) *bool {
	t.Helper()
	called := false
	original := runShutdownSpinner
	runShutdownSpinner = func(_ io.Writer, done <-chan error) error {
		called = true
		return <-done
	}
	t.Cleanup(func() { runShutdownSpinner = original })
	return &called
}

func TestWithShutdownProgressFastSkipsSpinner(t *testing.T) {
	called := stubShutdownSpinner(t)
	want := errors.New("boom")
	if err := WithShutdownProgress(io.Discard, func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if *called {
		t.Fatal("spinner ran for a cleanup that finished inside the delay")
	}
}

func TestWithShutdownProgressSlowRunsSpinnerAndReturnsCleanupError(t *testing.T) {
	called := stubShutdownSpinner(t)
	want := errors.New("cleanup failed")
	err := WithShutdownProgress(io.Discard, func() error {
		time.Sleep(shutdownSpinnerDelay + 100*time.Millisecond)
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if !*called {
		t.Fatal("spinner did not run for a cleanup that outlasted the delay")
	}
}

// A non-interactive writer has nothing to animate onto, so the spinner must
// degrade to a silent wait rather than starting a Bubble Tea program.
func TestRunShutdownSpinnerNonTerminalWaitsSilently(t *testing.T) {
	var out bytes.Buffer
	done := make(chan error, 1)
	want := errors.New("done")
	done <- want
	if err := runShutdownSpinner(&out, done); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %q, want nothing", out.String())
	}
}

// slowRuntime is a commandRuntime whose Cleanup outlasts the spinner delay.
type slowRuntime struct{}

func (slowRuntime) Services() appruntime.Services { return appruntime.Services{} }

func (slowRuntime) Cleanup() error {
	time.Sleep(shutdownSpinnerDelay + 100*time.Millisecond)
	return nil
}

// Regression: the spinner used to wrap main.go's deferred cleanup, but cobra's
// PersistentPostRunE cleans up first and nils activeRuntime, so the wrapper ran
// around a no-op and a slow drain showed nothing. Cleanup must draw wherever it
// actually happens.
func TestCleanupActiveRuntimeShowsProgress(t *testing.T) {
	called := stubShutdownSpinner(t)
	var active commandRuntime = slowRuntime{}
	if err := cleanupActiveRuntime(io.Discard, &active); err != nil {
		t.Fatalf("err = %v", err)
	}
	if active != nil {
		t.Fatal("activeRuntime not cleared")
	}
	if !*called {
		t.Fatal("cleanup did not run through the shutdown spinner")
	}
}
