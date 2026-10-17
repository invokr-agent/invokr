//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/support/testutil/clitest"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// TestX01_RetiredSpellingsFail covers plan case X01.5: a retired command
// spelling must fail loudly. Silently printing help (or worse, running the
// agent with "resume" as the prompt) leaves scripts broken but green.
func TestX01_RetiredSpellingsFail(t *testing.T) {
	provider := providertest.NewScripted(t)
	defer provider.Close()
	home := newHome(t, provider.URL())

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"provider key", []string{"provider", "key"}, "retired"},
		{"exec resume", []string{"exec", "resume"}, "retired"},
		{"chat", []string{"chat"}, "unknown command"},
		{"jobs add unknown flag", []string{
			"jobs", "add", "x", "--schedule", "* * * * *", "--prompt", "p", "--approve-all",
		}, "unknown flag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := clitest.Run(t, clitest.Options{
				State:       home.state,
				CommandArgs: append([]string{"--base-url=" + provider.URL()}, tc.args...),
				Env:         execEnv(nil),
			})
			if result.ExitCode == 0 {
				t.Fatalf("exit code = 0, want non-zero\nstdout: %s\nstderr: %s", result.Stdout, result.Stderr)
			}
			combined := result.Stdout + result.Stderr
			if !strings.Contains(strings.ToLower(combined), tc.want) {
				t.Fatalf("output does not mention %q:\n%s", tc.want, combined)
			}
		})
	}
	provider.AssertClean(t)
}
