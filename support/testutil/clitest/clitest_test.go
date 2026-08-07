package clitest

import (
	"strings"
	"testing"
)

func TestIsolatedEnvExcludesHostSecrets(t *testing.T) {
	t.Setenv("INVOKR_TEST_SECRET", "must-not-pass")
	state := NewState(t)
	env := strings.Join(isolatedEnv(state), "\n")
	if strings.Contains(env, "INVOKR_TEST_SECRET") || !strings.Contains(env, "HOME="+state.HomeDir) {
		t.Fatalf("unexpected isolated env: %s", env)
	}
}
