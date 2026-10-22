//go:build unix

package core

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ConfigureProcessGroup puts cmd in its own process group and arranges for
// context cancellation to kill the whole group, so grandchildren spawned by
// shell/code tools die with the direct child instead of surviving a timeout.
// WaitDelay bounds cmd.Wait against inherited pipes held open by orphans.
// It leaves cmd alone when a caller (e.g. a sandbox Build seam) already set
// SysProcAttr.
func ConfigureProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.SysProcAttr != nil {
		return
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		// Descendants that left the group (setsid/setpgid) are found by
		// parentage before the group kill orphans them to init.
		for _, pid := range descendants(cmd.Process.Pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			// The group is already gone: every member exited on its own (a shell
			// whose wait returned once its children were killed), so there is
			// nothing left to signal. That is a completed cancel, not a reason to
			// call the process done: os/exec treats ErrProcessDone from Cancel as
			// "the child was already reaped", and a Cancel that reports done while
			// the child exits 0 makes Wait return nil, which turns a cancelled run
			// into a successful one. Returning nil lets Wait report the context
			// error, which is what the caller needs to see that the run was cut
			// short rather than finished.
			return nil
		}
		return err
	}
	cmd.WaitDelay = time.Second
}

// awaitGroupGone re-kills cmd's process group until it has no members left or
// a short bound passes, so a timed-out or cancelled run does not return while
// a runaway tree (e.g. a self-re-executing launcher) is still forking.
func awaitGroupGone(cmd *exec.Cmd) {
	if cmd.Process == nil || cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		return
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if errors.Is(syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL), syscall.ESRCH) {
			return
		}
	}
}
