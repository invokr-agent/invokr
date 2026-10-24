package setup

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/support/fsutil"
)

const (
	homeDirPerm    os.FileMode = 0o700
	configFilePerm os.FileMode = 0o600
	memoryFilePerm os.FileMode = 0o600

	DefaultToolboxDir        = constants.DefaultToolboxDir
	DefaultSkillsDir         = constants.DefaultSkillsDir
	DefaultProjectMemoryFile = constants.ProjectMemoryFilename

	DefaultHomePrompt      = "~/.invokr"
	DefaultProviderBaseURL = constants.DefaultProviderBaseURL
)

// defaultPersonaTemplate seeds PERSONA.md. It mirrors
// constants.DefaultAgentSystemPrompt exactly: that constant is the single
// source of truth for the base prompt, so a home with no PERSONA.md and a
// home seeded by setup render the same system prompt. TestPersonaTemplate*
// pins the two together. The runtime appends environment, working rules,
// tools and skills after it, so none of that belongs here.
const defaultPersonaTemplate = constants.DefaultAgentSystemPrompt

type Paths struct {
	HomeDir, ConfigPath, ToolboxDir, SkillsDir, MemoryDir, ProfilesDir, PersonaPath, UserMemoryPath, ProjectMemoryPath string
}

type Result struct {
	HomeDir, ConfigPath, PersonaPath           string
	HomeCreated, ConfigCreated, PersonaCreated bool
}

// Answers are the values the questionnaire collects and writes to the config.
type Answers struct {
	ProviderBaseURL string
	// Model is the default model id. Empty keeps the built-in default, which is
	// what an offline provider or one without /v1/models leaves behind.
	Model string
	// MemoryEnabled records whether built-in Zerfoo memory recall was enabled.
	// Models download lazily on first use, so setup performs no install step.
	MemoryEnabled bool
	// OutsideReads sets workspace.reads to "ask": read and file_search may
	// reach paths beyond the workspace after the user approves each one.
	OutsideReads bool
	// SyntaxTheme is the chroma stylesheet for fenced code. It is optional:
	// empty leaves the UI to pick one matching the terminal background.
	SyntaxTheme string
	// InstallSkills records whether the curated starter skills should be
	// installed from the default hub. Interactive-only: scripted runs stay
	// off the network and leave it false.
	InstallSkills bool
	// ResetToolTemplates replaces edited copies during an explicit setup run.
	ResetToolTemplates bool
}

type QuestionnaireResult struct {
	Result
	Answers
}

func PathsFor(homeDir string) Paths {
	return Paths{
		HomeDir: homeDir, MemoryDir: homeDir,
		ConfigPath:     filepath.Join(homeDir, constants.DefaultConfigFile),
		ToolboxDir:     filepath.Join(homeDir, DefaultToolboxDir),
		SkillsDir:      filepath.Join(homeDir, DefaultSkillsDir),
		ProfilesDir:    config.ProfilesDir(homeDir),
		PersonaPath:    config.PersonaPath(homeDir),
		UserMemoryPath: filepath.Join(homeDir, constants.UserMemoryFilename),
		// ProjectMemoryPath is the legacy global PROJECT.md. It is no longer
		// read: project memory is scoped per workspace under
		// <MemoryDir>/projects (markdown.ProjectDirFor). The file is left on
		// disk untouched.
		ProjectMemoryPath: filepath.Join(homeDir, DefaultProjectMemoryFile),
	}
}

func Ensure() (Result, error) { return EnsureAt(config.DefaultConfigDir()) }

// EnsureFirstRun runs the setup questionnaire when the Invokr home does not
// exist yet and the session is interactive, so a fresh install is configured
// instead of silently getting defaults. Anything else (home already there,
// piped or scripted input) falls back to Ensure.
func EnsureFirstRun(in io.Reader, out io.Writer) (Result, error) {
	homeDir := config.DefaultConfigDir()
	exists, err := fsutil.Exists(homeDir)
	if err != nil {
		return Result{}, err
	}
	if exists || !fsutil.IsTerminal(in) {
		if !exists {
			_, _ = fmt.Fprintln(os.Stderr, "no config yet; run `invokr setup`")
		}
		return EnsureAt(homeDir)
	}
	result, err := RunQuestionnaire(in, out)
	return result.Result, err
}

func EnsureAt(homeDir string) (Result, error) { return ensureWith(homeDir, ensureDefaultConfig, false) }

// ensureWith is the shared body of EnsureAt and EnsureConfigured: ensure the
// home layout, write the config via writeConfig, then seed PERSONA.md.
func ensureWith(homeDir string, writeConfig func(path, homeDir string) (bool, error), resetTemplates bool) (Result, error) {
	result, err := ensureHome(homeDir)
	if err != nil {
		return result, err
	}

	created, err := writeConfig(result.ConfigPath, homeDir)
	if err != nil {
		return result, err
	}
	result.ConfigCreated = created
	created, err = ensurePersonaMarkdown(PathsFor(homeDir).PersonaPath)
	if err != nil {
		return result, err
	}
	result.PersonaCreated = created
	if err := WriteToolTemplates(homeDir, resetTemplates); err != nil {
		return result, err
	}
	return result, nil
}

func RunQuestionnaire(in io.Reader, out io.Writer) (QuestionnaireResult, error) {
	return RunQuestionnaireFrom(in, out, "")
}

// RunQuestionnaireFromExisting prefills questionnaire answers from the current
// config file before running the questionnaire. Existing values should not be
// silently replaced by first-run defaults.
func RunQuestionnaireFromExisting(in io.Reader, out io.Writer, baseURL string) (QuestionnaireResult, error) {
	if in == nil {
		return QuestionnaireResult{}, fmt.Errorf("input is required")
	}
	if out == nil {
		return QuestionnaireResult{}, fmt.Errorf("output is required")
	}
	cfg, existed, err := fsutil.ReadJSON[config.AppConfig](config.DefaultConfigPath())
	if err != nil {
		return QuestionnaireResult{}, fmt.Errorf("parse config %s: %w", config.DefaultConfigPath(), err)
	}
	answers := Answers{}
	if existed {
		answers = answersFromConfig(cfg)
	}
	return runQuestionnaire(in, out, baseURL, answers, existed)
}

// RunQuestionnaireFrom is RunQuestionnaire with the base URL question
// prefilled, e.g. from --base-url or INVOKR_BASE_URL; empty means the default.
func RunQuestionnaireFrom(in io.Reader, out io.Writer, baseURL string) (QuestionnaireResult, error) {
	return runQuestionnaire(in, out, baseURL, Answers{}, false)
}

// runQuestionnaire runs the questionnaire with optional answer prefills.
func runQuestionnaire(in io.Reader, out io.Writer, baseURL string, prefill Answers, prefilled bool) (QuestionnaireResult, error) {
	if in == nil {
		return QuestionnaireResult{}, fmt.Errorf("input is required")
	}
	if out == nil {
		return QuestionnaireResult{}, fmt.Errorf("output is required")
	}
	return runQuestionnaireHuh(in, out, wantsAccessible(in), baseURL, prefill, prefilled)
}

// answersFromConfig reads the raw config file and maps the settings that have
// setup questions onto questionnaire answers.
func answersFromConfig(cfg config.AppConfig) Answers {
	baseURL, model := currentProviderAndModel(cfg)
	return Answers{
		ProviderBaseURL: baseURL,
		Model:           model,
		MemoryEnabled:   cfg.Memory.Zerfoo.Enabled,
		OutsideReads:    cfg.Workspace.Reads == config.ReadsAsk,
		SyntaxTheme:     cfg.UI.SyntaxTheme,
	}
}

// currentProviderAndModel follows the router table when it is configured,
// otherwise it uses the single-provider fields.
func currentProviderAndModel(cfg config.AppConfig) (string, string) {
	if !cfg.Provider.Router.Enabled() {
		return cfg.Provider.BaseURL, cfg.Provider.Model
	}
	active := cmp.Or(cfg.Provider.Router.Active, soleRouterProfile(cfg.Provider.Router.Profiles))
	profile, ok := cfg.Provider.Router.Profiles[active]
	if !ok {
		return cfg.Provider.BaseURL, cfg.Provider.Model
	}
	provider, hasProvider := cfg.Provider.Router.Providers[profile.Provider]
	baseURL := cfg.Provider.BaseURL
	if hasProvider && provider.BaseURL != "" {
		baseURL = provider.BaseURL
	}
	model := cmp.Or(profile.Model, cfg.Provider.Model)
	return baseURL, model
}

// soleRouterProfile returns the only router profile name, used when the table
// has one profile and no active profile was written.
func soleRouterProfile(profiles map[string]config.RouterProfileConfig) string {
	if len(profiles) != 1 {
		return ""
	}
	for name := range profiles {
		return name
	}
	return ""
}

// wantsAccessible reports whether the questionnaire should use huh's
// accessible mode.
func wantsAccessible(in io.Reader) bool { return accessibleMode(fsutil.IsTerminal(in)) }

// accessibleMode decides the mode from whether input is a terminal: anything
// that is not one, and any terminal too dumb to redraw. The TERM check
// mirrors huh.NewForm's own auto-enable, which the forms cannot rely on
// because they set the mode explicitly. It is split out from wantsAccessible
// so the TERM branch — which only decides anything when input *is* a
// terminal — is testable without a pty.
func accessibleMode(isTerminal bool) bool { return !isTerminal || os.Getenv("TERM") == "dumb" }

func EnsureConfigured(homeDir string, answers Answers) (Result, error) {
	return ensureWith(homeDir, func(path, home string) (bool, error) {
		return writeConfiguredDefault(path, home, answers)
	}, answers.ResetToolTemplates)
}

func ensureHome(homeDir string) (Result, error) {
	paths := PathsFor(homeDir)
	result := Result{HomeDir: paths.HomeDir, ConfigPath: paths.ConfigPath, PersonaPath: paths.PersonaPath}

	info, err := os.Stat(homeDir)
	switch {
	case err == nil:
		if !info.IsDir() {
			return result, fmt.Errorf("home path %q exists but is not a directory", homeDir)
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(homeDir, homeDirPerm); err != nil {
			return result, err
		}
		result.HomeCreated = true
	default:
		return result, err
	}
	for _, dir := range []string{paths.ToolboxDir, paths.SkillsDir, paths.MemoryDir, paths.ProfilesDir} {
		if err := os.MkdirAll(dir, homeDirPerm); err != nil {
			return result, err
		}
	}
	return result, nil
}

// ensurePersonaMarkdown seeds PERSONA.md with the built-in default prompt
// so the base system prompt is an editable file from the first run on. Only an
// absent file is written; an existing one is the user's.
func ensurePersonaMarkdown(path string) (bool, error) {
	exists, err := fsutil.Exists(path)
	if err != nil || exists {
		return false, err
	}
	if err := os.WriteFile(path, []byte(defaultPersonaTemplate), memoryFilePerm); err != nil {
		return false, err
	}
	return true, nil
}

func ensureDefaultConfig(path string, homeDir string) (bool, error) {
	exists, err := fsutil.Exists(path)
	if err != nil || exists {
		return false, err
	}
	cfg := config.DefaultAppConfigForHome(homeDir)
	cfg.Workspace.Root = "" // see writeConfiguredDefault: never pin the setup-time cwd
	if err := fsutil.WriteJSON(path, cfg, configFilePerm); err != nil {
		return false, err
	}
	return true, nil
}

// defaultRouterConfig seeds the provider table a fresh setup starts with: the
// local llama.cpp server it just configured, plus a ready-to-activate Anthropic
// profile. Anthropic stays inert until the user switches to it with
// "invokr config use anthropic" and has the provider binary and API key in
// place, so seeding it costs nothing but makes the switch a one-liner.
//
// Only new configurations get this. An existing config without a router table
// keeps taking the single-provider path, so upgrading changes no behavior.
func defaultRouterConfig(provider config.ProviderConfig) config.RouterConfig {
	return config.RouterConfig{
		Active: "local",
		Providers: map[string]config.RouterProviderConfig{
			"local": {
				Type: "openai-compatible", BaseURL: provider.BaseURL,
				// llama.cpp serves the context window here; other providers
				// report nothing and invokr falls back to configuration.
				Options: map[string]any{"metadata_path": "/props"},
			},
			"anthropic": {Type: "anthropic", APIKeyEnv: "ANTHROPIC_API_KEY"},
		},
		Profiles: map[string]config.RouterProfileConfig{
			"local": {Provider: "local", Model: provider.Model}, "anthropic": {Provider: "anthropic", Model: constants.DefaultAnthropicModel},
		},
	}
}

func writeConfiguredDefault(path string, homeDir string, answers Answers) (bool, error) {
	existing, existed, err := fsutil.ReadJSON[config.AppConfig](path)
	if err != nil {
		return false, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg := config.DefaultAppConfigForHome(homeDir)
	// Never pin the setup-time cwd; a later run resolves the caller's cwd.
	cfg.Workspace.Root = ""

	cfg.Provider.BaseURL = cmp.Or(strings.TrimSpace(answers.ProviderBaseURL), existing.Provider.BaseURL, cfg.Provider.BaseURL)
	if model := strings.TrimSpace(answers.Model); model != "" {
		cfg.Provider.Model = model
	}
	cfg.Memory.Zerfoo.Enabled = answers.MemoryEnabled
	if answers.OutsideReads {
		cfg.Workspace.Reads = config.ReadsAsk
	} else {
		cfg.Workspace.Reads = cmp.Or(existing.Workspace.Reads, cfg.Workspace.Reads)
	}
	if theme := strings.TrimSpace(answers.SyntaxTheme); theme != "" {
		cfg.UI.SyntaxTheme = theme
	}

	// Preserve a custom router table when setup is re-run. If the existing
	// router is the one setup seeds, refresh it from the answered provider so
	// the answers are reflected everywhere.
	if existed && existing.Provider.Router.Enabled() && !isLocalSetupRouter(existing.Provider.Router) {
		cfg.Provider.Router = existing.Provider.Router
	} else {
		cfg.Provider.Router = defaultRouterConfig(cfg.Provider)
	}

	if err := fsutil.WriteJSON(path, cfg, configFilePerm); err != nil {
		return false, err
	}
	return !existed, nil
}

// isLocalSetupRouter reports whether the router is the one setup seeds, so
// re-running setup can update the answers without replacing a custom router
// table. It checks the stable shape: the local/anthropic profiles setup
// installs and a local active profile.
func isLocalSetupRouter(router config.RouterConfig) bool {
	if router.Active != "local" || len(router.Profiles) != 2 || len(router.Providers) != 2 {
		return false
	}
	// A missing entry is a zero value, which fails the field checks below.
	localProfile, anthropicProfile := router.Profiles["local"], router.Profiles["anthropic"]
	local, anthropic := router.Providers["local"], router.Providers["anthropic"]
	return localProfile.Provider == "local" && anthropicProfile.Provider == "anthropic" &&
		local.Type == "openai-compatible" && anthropic.Type == "anthropic" &&
		anthropic.APIKeyEnv == "ANTHROPIC_API_KEY" &&
		anthropicProfile.Model == constants.DefaultAnthropicModel
}

// defaultHomePrompt is the home-path question's default. It honors
// INVOKR_HOME when the process was launched with one, so accepting the
// default from an isolated or scripted run writes into that home rather than
// the operator's real one (~/.invokr).
func defaultHomePrompt() string {
	if dir := config.Env("INVOKR_HOME"); dir != "" {
		return dir
	}
	return DefaultHomePrompt
}

func expandHomePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultHomePrompt()
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return path, nil
}
