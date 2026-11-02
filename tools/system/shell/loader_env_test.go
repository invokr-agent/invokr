package shell

import (
	"strings"
	"testing"
)

// Each line ran a model-chosen command through git or a loader while
// classifying as read-only, so it never asked.
func TestPolicyRefusesEnvironmentThatRunsCommands(t *testing.T) {
	for _, line := range []string{
		"GIT_EXTERNAL_DIFF='touch x #' git diff",
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0='touch x' git status",
		"export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0='touch x'; git status",
		"GIT_CONFIG_GLOBAL=./evil.cfg git status",
		"GIT_DIR=/tmp/evil git status",
		"GIT_SSH_COMMAND=x git log",
		"HOME=./evil git status",
		"XDG_CONFIG_HOME=./evil git status",
		"LESSOPEN='|touch x %s' ls",
		"GCONV_PATH=. ls",
		"PAGER=sh git log",
	} {
		if _, err := classify(t, line); err == nil || !strings.Contains(err.Error(), "which program runs") {
			t.Errorf("%q: err = %v, want refusal", line, err)
		}
	}
}

func TestPolicyGitFlagsThatReachOutsideAreNotReadonly(t *testing.T) {
	for _, line := range []string{
		"git diff --no-index /etc/shadow /dev/null",
		"git diff --ext-diff",
		"git log -p --ext-diff",
		"git show --textconv HEAD",
		"git help --web status",
	} {
		if pol, err := classify(t, line); err == nil && pol.tier == shellTierReadonly {
			t.Errorf("%q classified read-only", line)
		}
	}
}
