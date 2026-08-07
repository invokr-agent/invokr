//go:build e2e

package e2e

import (
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/support/testutil/clitest"
)

func TestR10_MalformedConfigFailsNamed(t *testing.T) {
	home := newHome(t, "http://127.0.0.1:1")
	configPath := filepath.Join(home.state.HomeDir, "config.json")
	mustWrite(t, configPath, `{"provider":`)

	result := clitest.Run(t, clitest.Options{
		State:       home.state,
		CommandArgs: []string{"--help"},
		Timeout:     10 * time.Second,
	})
	if result.Err == nil || result.ExitCode == 0 || result.TimedOut {
		t.Fatalf("malformed config result: exit=%d timeout=%v err=%v", result.ExitCode, result.TimedOut, result.Err)
	}
	want := regexp.MustCompile(`parse config .*config\.json: unexpected end of JSON input`)
	if !want.MatchString(result.Stderr) {
		t.Fatalf("stderr = %q, want %s", result.Stderr, want)
	}
}
