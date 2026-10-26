package toolbox

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"github.com/invokr-ai/invokr/constants"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// daemonProcess is a long-lived subprocess that handles multiple tool calls
// over its lifetime. Communication is newline-delimited JSON on stdin/stdout.
// env is snapshotted at daemon startup; subsequent callers share the same env.
type daemonProcess struct {
	gate    chan struct{}
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	scanner *bufio.Scanner
	done    chan struct{} // closed by the reaper goroutine when the process exits
	stderr  *tailWriter
}

type daemonKey struct{ pluginDir, sessionID string }

var (
	daemonsMu sync.Mutex
	daemons   = make(map[daemonKey]*daemonProcess)
)

// getDaemon returns (creating if needed) the daemon process for pluginDir.
// The daemon's lifetime is independent of any caller context; use evictDaemon
// to shut it down.
func getDaemon(key daemonKey, maxOutputBytes int, build func() (*exec.Cmd, func(), error), env []string) (*daemonProcess, error) {
	daemonsMu.Lock()
	defer daemonsMu.Unlock()

	if dp := daemons[key]; daemonAlive(dp) {
		return dp, nil
	}

	dp, err := startDaemon(maxOutputBytes, build, env)
	if err != nil {
		return nil, err
	}
	daemons[key] = dp
	return dp, nil
}

func daemonAlive(dp *daemonProcess) bool {
	if dp == nil || dp.cmd == nil || dp.cmd.Process == nil {
		return false
	}
	select {
	case <-dp.done:
		return false
	default:
		return true
	}
}

func startDaemon(maxOutputBytes int, build func() (*exec.Cmd, func(), error), env []string) (*daemonProcess, error) {
	maxOutputBytes = cmp.Or(maxOutputBytes, constants.ToolboxDefaultMaxOutputBytes)
	cmd, cleanup, err := build()
	if err != nil {
		return nil, err
	}
	if cleanup == nil {
		cleanup = func() {}
	}
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("daemon stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("daemon stdout pipe: %w", err)
	}

	stderr := &tailWriter{limit: maxOutputBytes}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, fmt.Errorf("start daemon: %w", err)
	}

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait() // reaps the process; sets cmd.ProcessState
		cleanup()
		close(done)
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 4096), maxOutputBytes+1)

	return &daemonProcess{cmd: cmd, stdin: stdin, stdout: stdout, scanner: sc, done: done, stderr: stderr, gate: make(chan struct{}, 1)}, nil
}

// invokeDaemon sends a single JSON request to dp and returns the response text.
// It holds dp.gate for the full round-trip so concurrent callers are serialised
// (the daemon process is single-threaded). The call respects ctx: if ctx is
// cancelled the daemon is killed, the scan goroutine is drained, and an error
// is returned so the caller can evict and restart the daemon.
func invokeDaemon(ctx context.Context, dp *daemonProcess, reqBytes []byte) (string, error) {
	select {
	case dp.gate <- struct{}{}:
		defer func() { <-dp.gate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	var text string
	ch := make(chan error, 1)
	go func() {
		if _, err := dp.stdin.Write(append(bytes.TrimRight(reqBytes, "\n"), '\n')); err != nil {
			ch <- fmt.Errorf("write to daemon: %w", err)
			return
		}
		if !dp.scanner.Scan() {
			scanErr := dp.scanner.Err()
			if scanErr == nil {
				scanErr = fmt.Errorf("daemon closed stdout unexpectedly")
			}
			if detail := strings.TrimSpace(dp.stderr.String()); detail != "" {
				scanErr = fmt.Errorf("%w: %s", scanErr, detail)
			}
			ch <- fmt.Errorf("read from daemon: %w", scanErr)
			return
		}
		text = dp.scanner.Text()
		ch <- nil
	}()

	select {
	case err := <-ch:
		return text, err
	case <-ctx.Done():
		dp.kill()
		<-ch //nolint:errcheck // drain the scanner goroutine before releasing the lock
		return "", fmt.Errorf("daemon invoke: %w", ctx.Err())
	}
}

func (dp *daemonProcess) kill() {
	if dp.cmd != nil && dp.cmd.Process != nil {
		if dp.cmd.Cancel != nil {
			_ = dp.cmd.Cancel()
		} else {
			_ = dp.cmd.Process.Kill()
		}
		_ = dp.stdin.Close()
		_ = dp.stdout.Close()
	}
}

// evictDaemon removes the stale daemon entry for pluginDir so the next call
// restarts a fresh process. It only kills and deletes the entry when the
// currently-registered daemon is the same pointer as the one that failed —
// a concurrent caller may have already restarted a healthy replacement.
// The old process may already be dead; we ignore any kill error.
func evictDaemon(key daemonKey, failed *daemonProcess) {
	daemonsMu.Lock()
	defer daemonsMu.Unlock()
	if dp, ok := daemons[key]; ok && dp == failed {
		dp.kill()
		delete(daemons, key)
	}
}

// ShutdownDaemons terminates all long-lived toolbox processes. Runtime cleanup
// calls this once; it is safe to call repeatedly.
func ShutdownDaemons() {
	daemonsMu.Lock()
	all := daemons
	daemons = make(map[daemonKey]*daemonProcess)
	daemonsMu.Unlock()
	for _, dp := range all {
		dp.kill()
		// Killing the daemon doesn't guarantee its stdout pipe closes promptly —
		// a lingering grandchild (e.g. a headless browser) can hold it open and
		// hang runtime cleanup indefinitely. Bound the wait.
		select {
		case <-dp.done:
		case <-time.After(5 * time.Second):
		}
	}
}

type tailWriter struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	written := len(p)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) > w.limit {
		p = p[len(p)-w.limit:]
	}
	w.buf = append(w.buf, p...)
	w.buf = w.buf[max(0, len(w.buf)-w.limit):]
	return written, nil
}

func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

// parseDaemonResponse decodes the newline-delimited JSON response from the daemon.
func parseDaemonResponse(toolName, raw string) (string, error) {
	var result toolboxResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return "", fmt.Errorf("toolbox daemon %q: invalid response JSON: %w", toolName, err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("toolbox tool %q returned error: %s", toolName, result.Error)
	}
	return result.Result, nil
}
