package core

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRunCollectSuccess(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "echo out; echo err >&2")
	res, err := RunCollect(context.Background(), cmd, 1024)
	if err != nil {
		t.Fatalf("RunCollect: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "out" || strings.TrimSpace(res.Stderr) != "err" {
		t.Fatalf("unexpected output: stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
	if res.ExitCode != 0 || res.TimedOut || res.Truncated {
		t.Fatalf("unexpected classification: %+v", res)
	}
}

func TestRunProcessDirAndEnv(t *testing.T) {
	res, err := RunProcess(context.Background(), ProcessSpec{
		Name:        "/bin/sh",
		Args:        []string{"-c", "echo $FOO"},
		Dir:         t.TempDir(),
		Env:         []string{"FOO=bar"},
		OutputLimit: 1024,
	})
	if err != nil {
		t.Fatalf("RunProcess: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "bar" {
		t.Fatalf("env not applied: %q", res.Stdout)
	}
}

func TestRunProcessTimeout(t *testing.T) {
	res, err := RunProcess(context.Background(), ProcessSpec{
		Name:        "/bin/sh",
		Args:        []string{"-c", "sleep 5"},
		Timeout:     50 * time.Millisecond,
		OutputLimit: 1024,
	})
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if !res.TimedOut {
		t.Fatalf("TimedOut = false, want true: %+v", res)
	}
}

func TestRunCollectNonZeroExit(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 3")
	res, err := RunCollect(context.Background(), cmd, 1024)
	if err == nil {
		t.Fatalf("expected exit error")
	}
	if res.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
	}
	if res.TimedOut {
		t.Fatalf("unexpected TimedOut")
	}
}

func TestRunCollectTruncates(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "printf '%01000d' 0")
	res, err := RunCollect(context.Background(), cmd, 10)
	if err != nil {
		t.Fatalf("RunCollect: %v", err)
	}
	if !res.Truncated {
		t.Fatalf("expected Truncated")
	}
	if len(res.Stdout) > 10 {
		t.Fatalf("stdout not truncated: %d bytes", len(res.Stdout))
	}
}

func TestRunCollectTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 5")
	res, err := RunCollect(ctx, cmd, 1024)
	if err == nil {
		t.Fatalf("expected error on timeout")
	}
	if !res.TimedOut {
		t.Fatalf("expected TimedOut")
	}
	if res.ExitCode == 0 {
		t.Fatalf("expected non-zero ExitCode, got 0")
	}
}
