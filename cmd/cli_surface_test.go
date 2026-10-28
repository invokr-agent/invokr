package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/sessionstore"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/runtime"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func testRoot(t *testing.T) *cobra.Command {
	t.Helper()
	root, _ := newRootCmd(config.AppConfig{}, func(config.AppConfig, runtime.Options) (commandRuntime, error) {
		return &cleanupSpyRuntime{}, nil
	})
	return root
}

func TestGroupCommandsResolve(t *testing.T) {
	root := testRoot(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"bot", "serve"}, "serve"},
		{[]string{"provider", "set-key"}, "set-key"},
		{[]string{"skills", "rm"}, "rm"},
	} {
		found, _, err := root.Find(tc.args)
		if err != nil {
			t.Fatalf("Find(%v): %v", tc.args, err)
		}
		if found.Name() != tc.want {
			t.Fatalf("Find(%v) = %q, want %q", tc.args, found.Name(), tc.want)
		}
	}
}

// Pre-release: one name per thing. The spellings these replaced must not
// quietly do something, so a script written against them fails loudly
// instead. "chat" and "exec resume" fail this way: cobra genuinely has
// nowhere to route them, so Find leaves the word unconsumed and reports
// unknown-command. "provider key" instead resolves to an explicit retired
// stub (see TestRetiredSpellingsFailAtRuntime) — Find succeeds, but running
// it always errors, which is covered separately since Find can't see that.
func TestRetiredSpellingsDoNotResolve(t *testing.T) {
	root := testRoot(t)
	for _, args := range [][]string{
		{"chat"}, {"exec", "resume"}, {"history"}, {"session"}, {"job"}, {"init"}, {"run"}, {"reindex"},
		{"providers"}, {"config", "profile"}, {"config", "switch"}, {"jobs", "remove"}, {"jobs", "delete"}, {"skills", "remove"},
	} {
		// Find consumes the words it recognizes and hands back the rest. A
		// retired spelling must survive as an unconsumed argument — that is
		// what makes cobra report it as unknown rather than run something.
		_, rest, err := root.Find(args)
		last := args[len(args)-1]
		if err == nil && (len(rest) == 0 || rest[len(rest)-1] != last) {
			t.Errorf("%v still resolves to a command", args)
		}
	}
}

// Find() alone does not prove a retired spelling fails: a group command with
// no RunE prints its own help and exits 0 for any unrecognized child, and
// exec has no subcommands at all so "resume" always resolves to exec. Both
// must actually error at Execute() time instead of running something.
func TestRetiredSpellingsFailAtRuntime(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"provider", "key"}, "provider key is retired"},
		{[]string{"exec", "resume"}, "exec resume is retired"},
	} {
		root := testRoot(t)
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(tc.args)
		err := root.Execute()
		if err == nil {
			t.Fatalf("%v: want an error, got nil (exit 0)", tc.args)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: error = %q, want it to contain %q", tc.args, err.Error(), tc.want)
		}
	}
}

// A bare group command must do the obvious thing rather than print its own
// help; `invokr config` is the cheapest one to prove because it never
// bootstraps the runtime.
func TestBareConfigRunsShow(t *testing.T) {
	root := testRoot(t)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"config"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	// Bare config is a summary (A4); --json is the whole document.
	if !strings.Contains(out.String(), "provider") || !strings.Contains(out.String(), "model") || strings.Contains(out.String(), "{") {
		t.Fatalf("bare config should print the summary, got: %q", out.String())
	}
}

func TestGroupCommandsHaveDefaultAction(t *testing.T) {
	root := testRoot(t)
	for _, name := range []string{"sessions", "jobs", "config"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatalf("Find(%q): %v", name, err)
		}
		if cmd.RunE == nil {
			t.Fatalf("%q has no default action; a bare invocation would only print help", name)
		}
	}
}

// `bot serve` starts a long-running daemon that talks to the outside world, so
// it is the one group that must not act on a bare invocation.
func TestBotHasNoDefaultAction(t *testing.T) {
	cmd, _, err := testRoot(t).Find([]string{"bot"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.RunE != nil || cmd.Run != nil {
		t.Fatal("bare `invokr bot` must not start the daemon")
	}
}

func TestExecResumeFlagsMatchAgentSpelling(t *testing.T) {
	cmd := NewExecCmd(func() (runtime.Services, error) { return runtime.Services{}, nil }, &config.AppConfig{})
	for _, name := range []string{"resume", "last"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("exec is missing --%s", name)
		}
	}
}

// --max-turns writes straight into the config the runtime is built from, so a
// run can be given a bigger or smaller budget without touching config.json.
func TestExecMaxTurnsOverridesConfig(t *testing.T) {
	appConfig := &config.AppConfig{Agent: config.AgentConfig{MaxTurns: 40}}
	cmd := NewExecCmd(func() (runtime.Services, error) { return runtime.Services{}, nil }, appConfig)
	if err := cmd.Flags().Parse([]string{"--max-turns", "12"}); err != nil {
		t.Fatal(err)
	}
	if appConfig.Agent.MaxTurns != 12 {
		t.Fatalf("max turns = %d, want the flag value", appConfig.Agent.MaxTurns)
	}
}

func TestResolveResumeTargetPrefersResumeFlag(t *testing.T) {
	store := sessionstore.New(t.TempDir())
	id, args, err := resolveResumeTarget(store, execOptions{resumeID: "abc"}, []string{"a prompt"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "abc" {
		t.Fatalf("session id = %q, want abc", id)
	}
	if len(args) != 1 || args[0] != "a prompt" {
		t.Fatalf("prompt args = %v, want [a prompt]", args)
	}
}

func TestSessionLabelFallsBackToPreview(t *testing.T) {
	if got := sessionLabel(sessionstore.SessionInfo{Preview: "hello"}); got != "hello" {
		t.Fatalf("label = %q, want hello", got)
	}
	if got := sessionLabel(sessionstore.SessionInfo{}); got != "(untitled)" {
		t.Fatalf("label = %q, want (untitled)", got)
	}
}

func walkCommands(root *cobra.Command, visit func(*cobra.Command)) {
	visit(root)
	for _, child := range root.Commands() {
		walkCommands(child, visit)
	}
}

func TestEveryVisibleCommandDescribesItself(t *testing.T) {
	walkCommands(testRoot(t), func(cmd *cobra.Command) {
		if cmd.Hidden || cmd.Name() == "help" || cmd.Name() == "completion" {
			return
		}
		if strings.TrimSpace(cmd.Short) == "" {
			t.Errorf("%q has no Short; it renders as a blank line in its parent's help", cmd.CommandPath())
		}
		if strings.HasSuffix(cmd.Short, ".") {
			t.Errorf("%q Short ends in a period; the rest of the tree does not", cmd.CommandPath())
		}
	})
}

// pflag reads the first backquoted word of a flag's usage string as that
// flag's value placeholder, so a backtick meant as emphasis silently renames
// the type: `--resume invokr sessions` instead of `--resume string`.
func TestFlagUsageStringsAvoidBacktickPlaceholders(t *testing.T) {
	walkCommands(testRoot(t), func(cmd *cobra.Command) {
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if strings.Contains(f.Usage, "`") {
				t.Errorf("%s --%s: backtick in usage becomes the value placeholder: %q",
					cmd.CommandPath(), f.Name, f.Usage)
			}
		})
	})
}

// Args validation happens before PersistentPreRunE, so usage still prints for a
// mistyped command line; everything after it is a runtime error the usage block
// only buries.
func TestRuntimeErrorsDoNotPrintUsage(t *testing.T) {
	root := testRoot(t)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.AddCommand(&cobra.Command{
		Use:     "boom",
		GroupID: groupRun,
		RunE:    func(*cobra.Command, []string) error { return errors.New("exploded") },
	})
	root.SetArgs([]string{"boom"})
	if err := root.Execute(); err == nil {
		t.Fatal("want the command's error")
	}
	if strings.Contains(out.String(), "Usage:") {
		t.Fatalf("runtime failure printed the usage block:\n%s", out.String())
	}
}
