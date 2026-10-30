package cmd

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/logging"
	"github.com/invokr-agent/invokr/platform/version"
	"os"

	appruntime "github.com/invokr-agent/invokr/platform/runtime"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
)

// Root's own help text, kept out of the constructor so the wiring there stays
// readable as wiring.
const rootLong = "Invokr runs an LLM agent over your workspace: it talks to an\nOpenAI-compatible model, runs tools on your behalf under an approval\npolicy, and remembers the sessions it ran.\n\nRun it bare for the interactive TUI. `exec` is the same agent without\na terminal, for scripts and CI. Everything else inspects or configures\nthose two."

const rootExample = "  # start a new interactive session\n  invokr\n\n  # pick a stored session from a menu and continue it\n  invokr --resume\n\n  # one-shot run, no terminal needed\n  invokr exec \"summarize the open TODOs\"\n\n  # what ran before, and why it will not start\n  invokr sessions\n  invokr doctor"

type commandRuntime interface {
	Services() appruntime.Services
	Cleanup() error
}

type runtimeBootstrap func(config.AppConfig, appruntime.Options) (commandRuntime, error)

// NewRootCmd builds the root command and returns a cleanup function that tears
// down the active runtime. The cleanup is idempotent and bound to this command
// instance only, so callers should defer it to guarantee teardown even when the
// command returns an error (cobra skips PersistentPostRunE on failure).
func NewRootCmd(app config.AppConfig) (*cobra.Command, func() error) {
	return newRootCmd(app, func(app config.AppConfig, opts appruntime.Options) (commandRuntime, error) {
		return appruntime.Bootstrap(app, opts)
	})
}

func newRootCmd(app config.AppConfig, bootstrap runtimeBootstrap) (*cobra.Command, func() error) {
	appConfig := app.WithDefaults()
	logger := zap.NewNop()
	var activeRuntime commandRuntime
	var logBuffer *logging.Buffer
	var logView bool

	rootCmd := &cobra.Command{
		Use:     "invokr",
		Short:   "Invokr agent CLI",
		Long:    rootLong,
		Example: rootExample,
		Version: version.String(),
		// main prints the returned error once; cobra printing it too doubled it.
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			modePolicy, modeErr := startupMode(cmd)
			if modeErr != nil {
				return modeErr
			}
			// Past this point the command line itself parsed, so any error is a
			// runtime one and the usage block is noise above it. Errors from
			// flag and argument validation happen before PersistentPreRunE and
			// still print usage, which is the case where it helps.
			cmd.SilenceUsage = true
			if skipsRuntimeBootstrap(cmd) {
				return nil
			}
			// --base-url is the escape hatch back to single-provider mode: a
			// router table configured for daily use must not swallow a
			// deliberate one-off override (ad hoc testing, a mocked provider
			// in e2e). The flag wins outright rather than merging with the
			// active profile, which would leave an unrelated provider type
			// or model in play.
			if cmd.Root().PersistentFlags().Changed("base-url") {
				appConfig.Provider.Router = config.RouterConfig{}
			}
			// --allow-all means "nobody is watching, approve everything".
			// The shell's executable allowlist is a separate gate that would
			// otherwise still refuse npm/make/etc. outright, so lift it too;
			// those roots become write tier and the auto-approver decides.
			if flag := cmd.Flags().Lookup("allow-all"); flag != nil && flag.Value.String() == "true" {
				appConfig.Shell.AllowAnyCommand = true
				// The network tier is auto-approved either way; without this
				// the sandbox still unshares the netns and curl/wget cannot
				// resolve anything (#543).
				appConfig.Shell.AllowNetworkCommands = true
			}
			if isAgentCommand(cmd) && isTerminalInput(cmd) {
				configureAgentCommandLogging(&appConfig, &logBuffer, logView, cmd.Root().PersistentFlags().Changed("log-output"))
			}
			rt, err := bootstrap(appConfig, appruntime.Options{
				Policy:       modePolicy,
				LogBuffer:    logBuffer,
				LogView:      logView,
				ModelFlagSet: cmd.Root().PersistentFlags().Changed("model"),
				// ponytail: only commands that send turns need a model.
				ModelOptional: !isAgentCommand(cmd) && !hasAncestorNamed(cmd, "exec", "acp"),
			})
			if err != nil {
				return err
			}

			activeRuntime = rt
			logger = rt.Services().Logger

			logger.Named("cmd").Debug("command initialized", zap.String("base_url", appConfig.Provider.BaseURL), zap.String("model", appConfig.Provider.Model), zap.String("log_level", appConfig.Log.Level), zap.String("log_format", appConfig.Log.Format), zap.String("log_output", appConfig.Log.Output), zap.String("log_filter", appConfig.Log.Filter))
			return nil
		},
		PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
			return cleanupActiveRuntime(cmd.ErrOrStderr(), &activeRuntime)
		},
	}
	session := registerRootFlags(rootCmd, &appConfig, &logView)
	registerCommands(rootCmd, &appConfig, &activeRuntime, session)
	return rootCmd, func() error { return cleanupActiveRuntime(os.Stderr, &activeRuntime) }
}

// registerRootFlags registers the flags every command inherits, plus the help
// presentation that has to be set on root: the version template, the command
// groups, and a group for cobra's own generated commands (unassigned, they land
// in an "Additional Commands" bucket that exists for no other reason).
func registerRootFlags(rootCmd *cobra.Command, appConfig *config.AppConfig, logView *bool) *pflag.FlagSet {
	flags := rootCmd.PersistentFlags()
	flags.StringVar(&appConfig.Provider.BaseURL, "base-url", appConfig.Provider.BaseURL, "Base URL for the model/provider API")
	flags.StringVar(&appConfig.Provider.Model, "model", appConfig.Provider.Model, "Model name sent to the provider API")
	// --profile is consumed before cobra runs (main.go profileRequested): the
	// overlay must land under the flag bindings above. Registered here only so
	// cobra accepts the flag and documents it.
	flags.String("profile", "", "Run with a profile from ~/.invokr/profiles/<name>.md, <name>.json, or the router table")
	flags.StringVar(&appConfig.Log.Level, "log-level", appConfig.Log.Level, "Default log level: debug, info, warn, error, dpanic, panic, or fatal")
	flags.StringVar(&appConfig.Log.Format, "log-format", appConfig.Log.Format, "Log format: console or json")
	flags.StringVar(&appConfig.Log.Output, "log-output", appConfig.Log.Output, "Log output path, stdout, or stderr")
	flags.StringVar(&appConfig.Log.Filter, "log-filter", appConfig.Log.Filter, "Per-logger filter rules, e.g. 'agent.session=info,clients=debug' or 'info:agent.session,debug:clients'")
	// Session flags only mean something to a run of the agent: root, agent and
	// exec carry them as local flags (registerCommands), so other commands'
	// help does not list them.
	session := pflag.NewFlagSet("session", pflag.ContinueOnError)
	session.String("mode", cmp.Or(appConfig.SessionMode, "manual"), "Session mode: manual, auto, plan (agent/exec; exec denies operations that still need approval). Auto cannot initialize a repository at the workspace root; use Manual or your terminal first")
	session.Bool("yolo", false, "DANGER: bypass approvals and Invokr sandbox/workspace restrictions for this session")
	session.Bool("allow-publishing", false, "Allow recognized publishing operations in Auto for this session")
	session.IntVar(&appConfig.Provider.ContextWindowTokens, "context-window-tokens", appConfig.Provider.ContextWindowTokens, "Provider context window token limit for usage display")
	session.StringSliceVar(&appConfig.Tools.Allowlist, "tools", appConfig.Tools.Allowlist, "Advertise only these tools to the model, repeatable (also INVOKR_TOOLS); a narrow set cuts selection dilution on small models")
	session.BoolVar(&appConfig.Tools.TmuxHostServer, "use-host-tmux", appConfig.Tools.TmuxHostServer, "Point the tmux tool at your own tmux server, unsandboxed: the agent can read and type into all your sessions (also INVOKR_TMUX_HOST_SERVER=1)")
	session.BoolVar(logView, "log-view", false, "Show full-width Chat and Logs tabs during interactive agent runs")
	session.BoolVar(&appConfig.Sandbox.Enabled, "sandbox", appConfig.Sandbox.Enabled, "Re-exec the agent inside a docker container before bootstrapping (set INVOKR_SANDBOX=1 to enable via env); auto-build requires a Linux host")
	rootCmd.SetVersionTemplate("invokr {{.Version}}\n")
	rootCmd.AddGroup(rootCommandGroups()...)
	rootCmd.SetHelpCommandGroupID(groupConfigure)
	// Shell completion is a one-time install step, not a daily command: keep it
	// runnable, out of help.
	rootCmd.CompletionOptions.HiddenDefaultCmd = true
	return session
}

// registerCommands builds the command tree. The agent is wired twice on
// purpose: as the `agent` subcommand, and as root's own RunE so a bare `invokr`
// starts it.
func registerCommands(rootCmd *cobra.Command, appConfig *config.AppConfig, activeRuntime *commandRuntime, session *pflag.FlagSet) {
	services := runtimeServicesProvider(activeRuntime)
	agentCmd, execCmd := NewAgentCmd(services), NewExecCmd(services, appConfig)
	agentCmd.Flags().AddFlagSet(session)
	execCmd.Flags().AddFlagSet(session)
	// A bare invocation runs the interactive agent; NoArgs keeps mistyped
	// subcommands failing instead of silently launching the TUI.
	rootCmd.RunE = agentCmd.RunE
	// Borrow the agent's Args too: it still rejects mistyped subcommands, but
	// additionally accepts the one positional that `--resume <id>` produces.
	rootCmd.Args = agentCmd.Args
	// Share the agent's flag pointers so `invokr --resume/--last` binds the same
	// variables the borrowed RunE and Args close over. Must precede AddCommand,
	// which would otherwise merge root's persistent flags into agentCmd.Flags().
	rootCmd.Flags().AddFlagSet(agentCmd.Flags())
	rootCmd.AddCommand(agentCmd, execCmd, NewIndexCmd(services), NewSessionsCmd(services), NewJobsCmd(appConfig), NewSetupCmd(), NewTokfFilterStdinCmd(), NewACPCmd(services),
		NewConfigCmd(appConfig), NewDoctorCmd(appConfig), NewSkillsCmd(appConfig), NewToolboxCmd(appConfig), NewProviderCmd(), NewUpdateCmd(), &cobra.Command{
			Use: "version", Short: "Print the version (same as --version)", GroupID: groupConfigure, Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "invokr %s\n", cmd.Root().Version)
				return err
			},
		})
}

func runtimeServicesProvider(activeRuntime *commandRuntime) ServicesProvider {
	return func() (appruntime.Services, error) {
		if activeRuntime == nil || *activeRuntime == nil {
			return appruntime.Services{}, fmt.Errorf("runtime services are required")
		}
		return (*activeRuntime).Services(), nil
	}
}

// cleanupActiveRuntime is the single place runtime cleanup happens: both
// PersistentPostRunE and the closure NewRootCmd returns route through it, and
// whichever runs first nils activeRuntime so the other is a no-op. The
// shutdown spinner therefore belongs here, not around either caller.
func cleanupActiveRuntime(w io.Writer, activeRuntime *commandRuntime) error {
	if activeRuntime == nil || *activeRuntime == nil {
		return nil
	}
	rt := *activeRuntime
	*activeRuntime = nil
	err := WithShutdownProgress(w, rt.Cleanup)
	if errors.Is(err, ErrShutdownAbandoned) {
		// The user chose to skip the wait; that is not a command failure.
		return nil
	}
	return err
}

func skipsRuntimeBootstrap(cmd *cobra.Command) bool {
	// doctor diagnoses environments where bootstrap itself would fail, so it
	// must never bootstrap; provider commands only touch the install cache and
	// the config file. Jobs commands manage the store directly; `jobs run`
	// bootstraps its own runtime inside jobs.RunJob, so the root bootstrap
	// would create a second one. `update` only fetches a release and replaces
	// this binary, so it needs no model either.
	return hasAncestorNamed(cmd, "jobs", "config", "skills", "provider") ||
		slices.Contains([]string{"setup", "doctor", "version", "update"}, cmd.Name())
}

func hasAncestorNamed(cmd *cobra.Command, names ...string) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if slices.Contains(names, c.Name()) {
			return true
		}
	}
	return false
}

func isAgentCommand(cmd *cobra.Command) bool {
	// The bare root invocation delegates to the agent RunE, so it counts too.
	return cmd != nil && (cmd.Name() == "agent" || !cmd.HasParent())
}

func isTerminalInput(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	terminalIn, ok := cmd.InOrStdin().(interface{ Fd() uintptr })
	return ok && term.IsTerminal(terminalIn.Fd())
}

func configureAgentCommandLogging(appConfig *config.AppConfig, logBuffer **logging.Buffer, logView bool, logOutputChanged bool) {
	*logBuffer = nil
	if logView {
		*logBuffer = logging.NewBuffer(300)
	}
	if !logOutputChanged && appConfig.Log.Output == config.DefaultOutput {
		appConfig.Log.Output = config.DefaultLogPath()
	}
}
