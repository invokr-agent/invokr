//go:build linux

package core

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// After a timeout the child, a grandchild in its group, and a grandchild that
// escaped into its own session must all be gone.
func TestRunProcessTimeoutKillsWholeTree(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available")
	}
	pidFile := filepath.Join(t.TempDir(), "pids")
	res, _ := RunProcess(context.Background(), ProcessSpec{
		Name:    "sh",
		Args:    []string{"-c", "exec >" + pidFile + "; echo $$; sleep 30 & echo $!; setsid sleep 30 & echo $!; wait"},
		Timeout: 300 * time.Millisecond,
	})
	if !res.TimedOut {
		t.Fatalf("expected timeout, got %+v", res)
	}
	data, _ := os.ReadFile(pidFile)
	pids := strings.Fields(string(data))
	if len(pids) != 3 {
		t.Fatalf("expected 3 pids, got %q", data)
	}
	for _, p := range pids {
		pid, _ := strconv.Atoi(p)
		deadline := time.Now().Add(3 * time.Second)
		for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) && !isZombie(pid) {
			if time.Now().After(deadline) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatalf("pid %d survived the timeout", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func isZombie(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}
