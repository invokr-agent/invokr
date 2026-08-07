//go:build unix

package core

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// A backgrounded grandchild that inherits stdout and outlives the shell must
// not block RunProcess past the timeout: the process group is killed and
// WaitDelay bounds the pipe drain.
func TestRunProcessTimeoutKillsProcessGroup(t *testing.T) {
	start := time.Now()
	res, err := RunProcess(context.Background(), ProcessSpec{
		Name:    "sh",
		Args:    []string{"-c", "sleep 30 & sleep 30"},
		Timeout: 200 * time.Millisecond,
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected error from timed-out run, got nil (res=%+v)", res)
	}
	if !res.TimedOut {
		t.Fatalf("expected TimedOut=true, got %+v", res)
	}
	// 200ms timeout + 1s WaitDelay + slack; without group kill the inherited
	// stdout pipe keeps cmd.Run blocked for the full 30s sleep.
	if elapsed > 5*time.Second {
		t.Fatalf("RunProcess blocked %v; grandchild survived the timeout", elapsed)
	}
}

func TestConfigureProcessGroupRespectsExistingSysProcAttr(t *testing.T) {
	cmd := exec.Command("sh", "-c", "true")
	attr := &syscall.SysProcAttr{}
	cmd.SysProcAttr = attr
	ConfigureProcessGroup(cmd)
	if cmd.SysProcAttr != attr || cmd.SysProcAttr.Setpgid {
		t.Fatalf("expected preset SysProcAttr untouched, got %+v", cmd.SysProcAttr)
	}
}
