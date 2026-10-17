//go:build e2e && unix

package e2e

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/support/testutil/clitest"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

func TestPlainIdleSIGTERMExitsWithOpenStdin(t *testing.T) {
	provider := providertest.NewScripted(t)
	defer provider.Close()
	home := newHome(t, provider.URL())
	cmd := exec.Command(clitest.Binary(t), "agent")
	cmd.Dir = home.workspace
	cmd.Env = append(os.Environ(), "INVOKR_HOME="+home.state.HomeDir, "INVOKR_MEMORY_DIR="+home.state.MemoryDir, "INVOKR_TOKF_DISABLED=true")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("child cleanup timed out")
		}
	})
	ready := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "agent started") {
				close(ready)
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("plain prompt did not start")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		done <- err // keep cleanup able to reap the completed child
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "context canceled") {
			t.Fatalf("expected context-canceled exit, got %v: %s", err, stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SIGTERM left idle CLI alive while stdin remained open")
	}
}
