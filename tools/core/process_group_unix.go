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
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
}
