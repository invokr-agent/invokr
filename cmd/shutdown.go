package cmd

import (
	"errors"
	"io"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2/spinner"
	"github.com/charmbracelet/x/term"
)

// shutdownSpinnerDelay is how long cleanup may take before we say anything.
// Most exits finish well inside it and stay silent.
const shutdownSpinnerDelay = 300 * time.Millisecond

// ErrShutdownAbandoned reports that the user pressed ctrl+c while cleanup was
// still draining. The work was left unfinished on purpose, so callers should
// not treat it as a failure.
var ErrShutdownAbandoned = errors.New("shutdown abandoned by user")

// WithShutdownProgress runs fn (runtime cleanup, which waits on the memory
// curator's queued summarization) and, if it outlasts shutdownSpinnerDelay,
// draws a spinner on w until it returns. ctrl+c abandons the wait and returns
// ErrShutdownAbandoned; the cleanup goroutine is left running and dies with
// the process.
//
// ponytail: huh's spinner component rather than a hand-rolled Bubble Tea model
// — it already owns the program lifecycle and the ctrl+c interrupt. And a
// spinner, not a progress bar: the curator drains an unbounded queue with no
// total to count against.
func WithShutdownProgress(w io.Writer, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()

	select {
	case err := <-done:
		return err
	case <-time.After(shutdownSpinnerDelay):
	}

	return runShutdownSpinner(w, done)
}

// runShutdownSpinner is a variable so tests can observe that the progress path
// was taken without needing a pty.
var runShutdownSpinner = func(w io.Writer, done <-chan error) error {
	if !isShutdownTerminal(w) {
		// Not an interactive terminal: nothing to animate onto and no key to
		// press, so just wait it out.
		return <-done
	}

	var cleanupErr error
	err := spinner.New().Title(" finishing up… (ctrl+c to skip)").WithOutput(w).WithInput(os.Stdin).Action(func() { cleanupErr = <-done }).Run()
	if errors.Is(err, tea.ErrInterrupted) || errors.Is(err, tea.ErrProgramKilled) {
		return ErrShutdownAbandoned
	}
	if err != nil {
		// The spinner failing must not mask cleanup, which is still running.
		return <-done
	}
	return cleanupErr
}

// isShutdownTerminal reports whether w is an interactive terminal, which is
// what makes an animated spinner worth drawing.
func isShutdownTerminal(w io.Writer) bool {
	f, ok := w.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(f.Fd())
}
