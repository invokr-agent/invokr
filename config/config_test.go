package config

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/invokr-agent/invokr/constants"
)

func configFromEnv() AppConfig {
	cfg, err := resolveLoadedConfig(AppConfig{}, "")
	if err != nil {
		panic(err)
	}
	return cfg
}

func TestWithDefaultsDoesNotReapplyEnvironment(t *testing.T) {
	t.Setenv("INVOKR_MODEL", "env-model")
	cfg := configFromEnv()
	if cfg.Provider.Model != "env-model" {
		t.Fatalf("loaded model = %q, want env-model", cfg.Provider.Model)
	}

	t.Setenv("INVOKR_MODEL", "changed-model")
	cfg = cfg.WithDefaults()
	if cfg.Provider.Model != "env-model" {
		t.Fatalf("WithDefaults reapplied environment: model = %q", cfg.Provider.Model)
	}
}

func TestProviderConfigDefaultsAndEnvOverrides(t *testing.T) {
	t.Setenv("INVOKR_CONTEXT_WINDOW_TOKENS", "128000")
	t.Setenv("INVOKR_PROVIDER_API_KEY", "secret")
	t.Setenv("INVOKR_PROVIDER_MAX_RETRIES", "3")
	t.Setenv("INVOKR_PROVIDER_HTTP_TIMEOUT", "30s")
	t.Setenv("INVOKR_PROVIDER_STALL_TIMEOUT", "2s")

	cfg := configFromEnv()

	if cfg.Provider.ContextWindowTokens != 128000 {
		t.Fatalf("expected context window override, got %d", cfg.Provider.ContextWindowTokens)
	}
	if cfg.Provider.APIKey != "secret" {
		t.Fatalf("expected provider API key override, got %q", cfg.Provider.APIKey)
	}
	if cfg.Provider.MaxRetries != 3 {
		t.Fatalf("expected provider retries override, got %d", cfg.Provider.MaxRetries)
	}
	if cfg.Provider.HTTPTimeout.String() != "30s" {
		t.Fatalf("expected provider timeout override, got %s", cfg.Provider.HTTPTimeout)
	}
	if cfg.Provider.StallTimeout.String() != "2s" {
		t.Fatalf("expected provider stall timeout override, got %s", cfg.Provider.StallTimeout)
	}
}

func TestProviderConfigDefaultsRetries(t *testing.T) {
	cfg := ProviderConfig{}.withDefaults()

	if cfg.MaxRetries != constants.DefaultProviderRetries {
		t.Fatalf("expected default provider retries %d, got %d", constants.DefaultProviderRetries, cfg.MaxRetries)
	}
}

func TestAgentConfigDefaultsAndEnvOverrides(t *testing.T) {
	t.Setenv("INVOKR_SYSTEM_PROMPT", "configured prompt")
	t.Setenv("INVOKR_SUMMARIZER_MODEL", "summary-model")
	t.Setenv("INVOKR_CONTEXT_WARNING_PERCENT", "60")
	t.Setenv("INVOKR_CONTEXT_COMPACT_PERCENT", "75")
	t.Setenv("INVOKR_AGENT_MAX_TURNS", "3")

	cfg := configFromEnv().Agent

	if cfg.SystemPrompt != "configured prompt" {
		t.Fatalf("expected configured prompt, got %q", cfg.SystemPrompt)
	}
	if cfg.SummarizerModel != "summary-model" {
		t.Fatalf("expected summary model, got %q", cfg.SummarizerModel)
	}
	if cfg.MaxTurns != 3 {
		t.Fatalf("expected configured max turns, got %d", cfg.MaxTurns)
	}
	if cfg.ContextWarningPercent != 60 || cfg.ContextCompactPercent != 75 {
		t.Fatalf("context thresholds = %d/%d, want 60/75", cfg.ContextWarningPercent, cfg.ContextCompactPercent)
	}
}

func TestAgentDefaultsIncludeRequestTokenCap(t *testing.T) {
	cfg := AppConfig{}.WithDefaults().Agent
	if cfg.ContextCompactPercent != 99 {
		t.Fatalf("ContextCompactPercent = %d, want 99", cfg.ContextCompactPercent)
	}
	if cfg.MaxRequestTokens != constants.DefaultAgentMaxRequestTokens {
		t.Fatalf("MaxRequestTokens = %d, want the default backstop %d", cfg.MaxRequestTokens, constants.DefaultAgentMaxRequestTokens)
	}
	if cfg.MaxTurns != constants.DefaultAgentMaxTurns {
		t.Fatalf("MaxTurns = %d, want %d", cfg.MaxTurns, constants.DefaultAgentMaxTurns)
	}
}

func TestToolboxConfigTokfDefaults(t *testing.T) {
	cfg := AppConfig{}.WithDefaults().Toolbox

	if cfg.TokfDisabled {
		t.Fatal("expected tokf to be enabled by default")
	}
	if cfg.TokfBin != constants.DefaultTokfBin {
		t.Fatalf("expected default tokf bin %q, got %q", constants.DefaultTokfBin, cfg.TokfBin)
	}
	if cfg.TokfHome != "" {
		t.Fatalf("expected empty default tokf home, got %q", cfg.TokfHome)
	}
}

func TestToolboxSupplyChainDefaults(t *testing.T) {
	cfg := AppConfig{}.WithDefaults().Toolbox
	if cfg.PackageIndexURL != "https://pypi.org/simple" || cfg.PackageMetadataURL != "https://pypi.org/pypi/%s/json" || cfg.ReleaseCooldownDays != 14 {
		t.Fatalf("unexpected supply-chain defaults: %#v", cfg)
	}
	if len(cfg.PackageIndexHosts) != 1 || cfg.PackageIndexHosts[0] != "pypi.org" || len(cfg.PackageArtifactHosts) != 1 || cfg.PackageArtifactHosts[0] != "files.pythonhosted.org" {
		t.Fatalf("unexpected allowed package hosts: %#v %#v", cfg.PackageIndexHosts, cfg.PackageArtifactHosts)
	}
}

func TestMemoryZerfooConfigDefaultsAndEnvOverrides(t *testing.T) {
	cfg := AppConfig{}.WithDefaults().Memory.Zerfoo
	if cfg.Enabled {
		t.Fatal("expected zerfoo memory disabled by default")
	}
	if cfg.EmbeddingModel != DefaultZerfooEmbeddingModel {
		t.Fatalf("expected default embedding model, got %q", cfg.EmbeddingModel)
	}
	if cfg.SummarizationModel != DefaultZerfooSummarizationModel {
		t.Fatalf("expected default summarization model, got %q", cfg.SummarizationModel)
	}
	if cfg.EmbeddingDimensions != DefaultZerfooEmbeddingDims {
		t.Fatalf("expected default embedding dims, got %d", cfg.EmbeddingDimensions)
	}
	if cfg.RequestTimeoutMS != 120000 || cfg.MaxRawEventsBeforeSummary != 12 || cfg.MaxSummaryTokens != 512 {
		t.Fatalf("unexpected zerfoo defaults: %#v", cfg)
	}

	t.Setenv("INVOKR_MEMORY_ZERFOO_ENABLED", "true")
	t.Setenv("INVOKR_MEMORY_ZERFOO_EMBEDDING_MODEL", "Custom/Embed")
	t.Setenv("INVOKR_MEMORY_ZERFOO_SUMMARIZATION_MODEL", "Custom/Sum")
	t.Setenv("INVOKR_MEMORY_ZERFOO_EMBEDDING_PATH", "/models/embed.gguf")
	t.Setenv("INVOKR_MEMORY_ZERFOO_SUMMARIZATION_PATH", "/models/sum.gguf")
	t.Setenv("INVOKR_MEMORY_ZERFOO_EMBEDDING_DIMENSIONS", "768")
	t.Setenv("INVOKR_MEMORY_ZERFOO_MAX_SUMMARY_TOKENS", "256")
	t.Setenv("INVOKR_MEMORY_ZERFOO_MAX_RAW_EVENTS_BEFORE_SUMMARY", "20")
	t.Setenv("INVOKR_MEMORY_ZERFOO_REQUEST_TIMEOUT_MS", "3000")

	cfg = configFromEnv().Memory.Zerfoo
	if !cfg.Enabled || cfg.EmbeddingModel != "Custom/Embed" || cfg.SummarizationModel != "Custom/Sum" {
		t.Fatalf("unexpected zerfoo env basics: %#v", cfg)
	}
	if cfg.EmbeddingPath != "/models/embed.gguf" || cfg.SummarizationPath != "/models/sum.gguf" {
		t.Fatalf("unexpected zerfoo env paths: %#v", cfg)
	}
	if cfg.EmbeddingRef() != "/models/embed.gguf" {
		t.Fatalf("EmbeddingRef should prefer local path, got %q", cfg.EmbeddingRef())
	}
	if cfg.EmbeddingDimensions != 768 || cfg.MaxSummaryTokens != 256 || cfg.MaxRawEventsBeforeSummary != 20 || cfg.RequestTimeoutMS != 3000 {
		t.Fatalf("unexpected zerfoo env sizing: %#v", cfg)
	}
}

func TestMemoryForgettingConfigDefaultsAndEnvOverrides(t *testing.T) {
	cfg := AppConfig{}.WithDefaults().Memory.Forgetting
	if cfg.Enabled {
		t.Fatal("expected forgetting disabled by default")
	}
	if cfg.HalfLifeDays != DefaultForgettingHalfLifeDays ||
		cfg.MinRetention != DefaultForgettingMinRetention ||
		cfg.ReinforcementFactor != DefaultForgettingReinforcementFactor ||
		cfg.MaxStrength != DefaultForgettingMaxStrength ||
		cfg.RawEventRetentionDays != DefaultForgettingRawEventRetentionDays {
		t.Fatalf("unexpected forgetting defaults: %#v", cfg)
	}
	if cfg.PurgeAfterDays != 0 {
		t.Fatalf("expected purge_after_days default 0, got %d", cfg.PurgeAfterDays)
	}
	if len(cfg.ExemptKinds) != 3 || cfg.ExemptKinds[0] != "preference" || cfg.ExemptKinds[1] != "profile" || cfg.ExemptKinds[2] != "doc" {
		t.Fatalf("unexpected default exempt kinds: %#v", cfg.ExemptKinds)
	}

	t.Setenv("INVOKR_MEMORY_FORGETTING_ENABLED", "true")
	t.Setenv("INVOKR_MEMORY_FORGETTING_HALF_LIFE_DAYS", "30")
	t.Setenv("INVOKR_MEMORY_FORGETTING_MIN_RETENTION", "0.25")
	t.Setenv("INVOKR_MEMORY_FORGETTING_REINFORCEMENT_FACTOR", "2.0")
	t.Setenv("INVOKR_MEMORY_FORGETTING_MAX_STRENGTH", "5.0")
	t.Setenv("INVOKR_MEMORY_FORGETTING_PURGE_AFTER_DAYS", "90")
	t.Setenv("INVOKR_MEMORY_FORGETTING_RAW_EVENT_RETENTION_DAYS", "3")
	t.Setenv("INVOKR_MEMORY_FORGETTING_EXEMPT_KINDS", "preference,decision")

	cfg = configFromEnv().Memory.Forgetting
	if !cfg.Enabled {
		t.Fatal("expected forgetting enabled via env")
	}
	if cfg.HalfLifeDays != 30 || cfg.MinRetention != 0.25 || cfg.ReinforcementFactor != 2.0 ||
		cfg.MaxStrength != 5.0 || cfg.PurgeAfterDays != 90 || cfg.RawEventRetentionDays != 3 {
		t.Fatalf("unexpected forgetting env overrides: %#v", cfg)
	}
	if len(cfg.ExemptKinds) != 2 || cfg.ExemptKinds[0] != "preference" || cfg.ExemptKinds[1] != "decision" {
		t.Fatalf("unexpected env exempt kinds: %#v", cfg.ExemptKinds)
	}
}

func TestToolboxConfigTokfEnvOverrides(t *testing.T) {
	t.Setenv("INVOKR_TOKF_ENABLED", "false")
	t.Setenv("INVOKR_TOKF_BIN", "/opt/tokf")
	t.Setenv("INVOKR_TOKF_HOME", "/tmp/tokf-home")

	cfg := configFromEnv().Toolbox

	if !cfg.TokfDisabled {
		t.Fatal("expected tokf env override to disable filtering")
	}
	if cfg.TokfBin != "/opt/tokf" {
		t.Fatalf("expected tokf bin env override, got %q", cfg.TokfBin)
	}
	if cfg.TokfHome != "/tmp/tokf-home" {
		t.Fatalf("expected tokf home env override, got %q", cfg.TokfHome)
	}
}

func TestWorkspaceDefaults(t *testing.T) {
	root := t.TempDir()
	t.Setenv("INVOKR_HOME", root)

	cfg := configFromEnv()
	if cfg.Workspace.Root == "" {
		t.Fatal("expected workspace root default")
	}
}

func TestWorkspaceEnvOverrides(t *testing.T) {
	t.Setenv("INVOKR_WORKSPACE_ROOT", "/tmp/workspace")
	t.Setenv("INVOKR_WORKSPACE_EXTRA_ROOTS", "/tmp/one,/tmp/two")
	t.Setenv("INVOKR_WORKSPACE_ALLOW_HIDDEN_PATHS", "true")

	cfg := configFromEnv()
	if cfg.Workspace.Root != "/tmp/workspace" {
		t.Fatalf("expected workspace root override, got %q", cfg.Workspace.Root)
	}
	if !slices.Equal(cfg.Workspace.ExtraRoots, []string{"/tmp/one", "/tmp/two"}) {
		t.Fatalf("expected workspace extra roots override, got %#v", cfg.Workspace.ExtraRoots)
	}
	if !cfg.Workspace.AllowHiddenPaths {
		t.Fatal("expected workspace hidden path override")
	}
}

func TestToolboxConfigInvalidTokfEnabledEnvKeepsFallback(t *testing.T) {
	t.Setenv("INVOKR_TOKF_ENABLED", "not-a-bool")

	cfg := configFromEnv().Toolbox

	if cfg.TokfDisabled {
		t.Fatal("expected invalid tokf enabled env to keep default true")
	}
}

func TestToolboxConfigCanDisableTokfWithStruct(t *testing.T) {
	cfg := AppConfig{Toolbox: ToolboxConfig{TokfDisabled: true}}.WithDefaults().Toolbox

	if !cfg.TokfDisabled {
		t.Fatal("expected struct config to disable tokf")
	}
}

func TestShellConfigDefaultsAndEnvOverrides(t *testing.T) {
	cfg := AppConfig{}.WithDefaults().Shell

	if !cfg.IsEnabled() {
		t.Fatal("expected shell to be enabled by default")
	}
	if !slices.Equal(cfg.AllowedCommands, constants.DefaultShellAllowedCommands()) {
		t.Fatalf("expected the default shell allowlist, got %#v", cfg.AllowedCommands)
	}
	if cfg.AllowWriteCommands {
		t.Fatal("expected write commands to require approval by default")
	}
	if cfg.AllowNetworkCommands {
		t.Fatal("expected network commands to require approval by default")
	}

	t.Setenv("INVOKR_SHELL_ALLOWED_COMMANDS", "ls,git,go")
	t.Setenv("INVOKR_SHELL_ALLOW_WRITE_COMMANDS", "true")
	t.Setenv("INVOKR_SHELL_ALLOW_NETWORK_COMMANDS", "true")
	cfg = configFromEnv().Shell
	if !slices.Equal(cfg.AllowedCommands, []string{"ls", "git", "go"}) {
		t.Fatalf("expected shell command env override, got %#v", cfg.AllowedCommands)
	}
	if !cfg.AllowWriteCommands || !cfg.AllowNetworkCommands {
		t.Fatalf("expected shell approval env overrides, got %#v", cfg)
	}
}

func TestShellConfigCanBeDisabledWithEnv(t *testing.T) {
	t.Setenv("INVOKR_SHELL_DISABLED", "true")

	cfg := configFromEnv().Shell

	if cfg.IsEnabled() {
		t.Fatal("expected shell disabled env override")
	}
}

func TestWebSearchConfigDefaultsAndEnvOverrides(t *testing.T) {
	cfg := AppConfig{}.WithDefaults().WebSearch
	if !slices.Equal(cfg.Providers, []string{"searxng"}) {
		t.Fatalf("expected default web search providers, got %#v", cfg.Providers)
	}
	if cfg.CustomHTTP.Method != constants.CustomHTTPSearchDefaultMethod || cfg.CustomHTTP.QueryParam != constants.CustomHTTPSearchDefaultQueryParam || cfg.CacheDir != constants.WebSearchDefaultCacheDir {
		t.Fatalf("unexpected web search defaults: %#v", cfg)
	}

	t.Setenv("INVOKR_WEB_SEARCH_PROVIDERS", "custom_http,brave")
	t.Setenv("INVOKR_WEB_SEARCH_CACHE_DIR", "/tmp/web-cache")
	t.Setenv("INVOKR_WEB_SEARCH_ALLOW_SENSITIVE", "true")
	t.Setenv("INVOKR_WEB_SEARCH_SENSITIVE_TERMS", "internal,secret")
	t.Setenv("INVOKR_WEB_SEARCH_PRIVATE_HOSTS", "git.example.test")
	t.Setenv("INVOKR_WEB_SEARCH_PRIVATE_REPO_NAMES", "private/repo")
	t.Setenv("INVOKR_WEB_SEARCH_CUSTOM_HTTP_ENDPOINT", "https://custom.local")
	t.Setenv("INVOKR_WEB_SEARCH_CUSTOM_HTTP_METHOD", "POST")
	t.Setenv("INVOKR_WEB_SEARCH_CUSTOM_HTTP_QUERY_PARAM", "query")
	t.Setenv("INVOKR_WEB_SEARCH_CUSTOM_HTTP_HEADERS_ENV", "Authorization=CUSTOM_AUTH")
	t.Setenv("INVOKR_WEB_SEARCH_CUSTOM_HTTP_MAPPINGS", "results=$.data,title=$.name")
	t.Setenv("INVOKR_WEB_SEARCH_SEARXNG_ENDPOINT", "http://searx.local/search")
	t.Setenv("INVOKR_WEB_SEARCH_SEARXNG_PORT", "9999")
	t.Setenv("INVOKR_WEB_SEARCH_DISABLED", "true")

	cfg = configFromEnv().WebSearch
	if !slices.Equal(cfg.Providers, []string{"custom_http", "brave"}) {
		t.Fatalf("expected provider env override, got %#v", cfg.Providers)
	}
	if cfg.CacheDir != "/tmp/web-cache" || !cfg.AllowSensitive {
		t.Fatalf("expected cache/sensitive env overrides, got %#v", cfg)
	}
	if !slices.Equal(cfg.SensitiveTerms, []string{"internal", "secret"}) ||
		!slices.Equal(cfg.PrivateHosts, []string{"git.example.test"}) ||
		!slices.Equal(cfg.PrivateRepoNames, []string{"private/repo"}) {
		t.Fatalf("expected sensitive env lists, got %#v", cfg)
	}
	if cfg.CustomHTTP.Endpoint != "https://custom.local" || cfg.CustomHTTP.Method != "POST" || cfg.CustomHTTP.QueryParam != "query" {
		t.Fatalf("expected custom HTTP env override, got %#v", cfg.CustomHTTP)
	}
	if cfg.CustomHTTP.HeadersEnv["Authorization"] != "CUSTOM_AUTH" || cfg.CustomHTTP.Mappings["results"] != "$.data" || cfg.CustomHTTP.Mappings["title"] != "$.name" {
		t.Fatalf("expected custom HTTP maps, got %#v", cfg.CustomHTTP)
	}
	if cfg.SearXNG.Endpoint != "http://searx.local/search" || cfg.SearXNG.Port != 9999 || cfg.SearXNG.Image != constants.SearXNGDefaultImage {
		t.Fatalf("expected searxng env override with defaults, got %#v", cfg.SearXNG)
	}
	if !cfg.Disabled {
		t.Fatal("expected INVOKR_WEB_SEARCH_DISABLED to disable web search")
	}
}

func TestMemoryConfigDefaults(t *testing.T) {
	root := t.TempDir()
	t.Setenv("INVOKR_HOME", root)

	cfg := configFromEnv().Memory

	if cfg.Dir != root {
		t.Fatalf("expected memory dir under INVOKR_HOME, got %q", cfg.Dir)
	}
}

func TestToolboxConfigDirDefaultsUnderHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("INVOKR_HOME", root)

	cfg := configFromEnv()
	if cfg.Toolbox.Dir != filepath.Join(root, constants.DefaultToolboxDir) {
		t.Fatalf("expected toolbox dir under INVOKR_HOME, got %q", cfg.Toolbox.Dir)
	}

	t.Setenv("INVOKR_TOOLBOX_DIR", filepath.Join(root, "custom-toolbox"))
	cfg = configFromEnv()
	if cfg.Toolbox.Dir != filepath.Join(root, "custom-toolbox") {
		t.Fatalf("expected toolbox dir env override, got %q", cfg.Toolbox.Dir)
	}
}

func TestDefaultAppConfigForHomeUsesExplicitHome(t *testing.T) {
	envHome := t.TempDir()
	explicitHome := t.TempDir()
	t.Setenv("INVOKR_HOME", envHome)

	cfg := DefaultAppConfigForHome(explicitHome)

	if cfg.Memory.Dir != explicitHome {
		t.Fatalf("expected memory dir under explicit home, got %q", cfg.Memory.Dir)
	}
	if cfg.Skills.Dir != filepath.Join(explicitHome, constants.DefaultSkillsDir) {
		t.Fatalf("expected skills dir under explicit home, got %q", cfg.Skills.Dir)
	}
}

func TestSkillsConfigDefaultsAndEnvOverrides(t *testing.T) {
	root := t.TempDir()
	t.Setenv("INVOKR_HOME", root)

	cfg := configFromEnv()
	if cfg.Skills.Dir != filepath.Join(root, constants.DefaultSkillsDir) {
		t.Fatalf("expected skills dir under INVOKR_HOME, got %q", cfg.Skills.Dir)
	}

	t.Setenv("INVOKR_SKILLS_DIR", filepath.Join(root, "custom-skills"))
	cfg = configFromEnv()
	if cfg.Skills.Dir != filepath.Join(root, "custom-skills") {
		t.Fatalf("expected skills dir env override, got %q", cfg.Skills.Dir)
	}
}

func TestToolboxDirBareRelativeResolvesAgainstHome(t *testing.T) {
	cfg := ToolboxConfig{Dir: "toolbox"}.WithDefaults("/home/u/.invokr")
	if want := "/home/u/.invokr/toolbox"; cfg.Dir != want {
		t.Fatalf("bare relative dir: got %q, want %q", cfg.Dir, want)
	}
}

func TestToolboxDirExplicitlyRelativeStaysCWDRelative(t *testing.T) {
	for _, dir := range []string{"./toolbox", "../shared/toolbox"} {
		if cfg := (ToolboxConfig{Dir: dir}).WithDefaults("/home/u/.invokr"); cfg.Dir != dir {
			t.Fatalf("dir %q: got %q, want it unchanged", dir, cfg.Dir)
		}
	}
}

func TestToolboxDirAbsoluteIsUnchanged(t *testing.T) {
	if cfg := (ToolboxConfig{Dir: "/opt/tools"}).WithDefaults("/home/u/.invokr"); cfg.Dir != "/opt/tools" {
		t.Fatalf("absolute dir: got %q, want /opt/tools", cfg.Dir)
	}
}

func TestMemoryProjectDirEnvOverride(t *testing.T) {
	t.Setenv("INVOKR_MEMORY_PROJECT_DIR", "/custom/projects")

	cfg := configFromEnv().Memory
	if cfg.ProjectDir != "/custom/projects" {
		t.Fatalf("ProjectDir = %q, want /custom/projects", cfg.ProjectDir)
	}
}

func TestToolResultGateDefaults(t *testing.T) {
	cfg := AppConfig{}.WithDefaults()
	if cfg.Agent.ToolResultMaxTokens != constants.ToolResultGateTokens {
		t.Fatalf("ToolResultMaxTokens default = %d, want %d", cfg.Agent.ToolResultMaxTokens, constants.ToolResultGateTokens)
	}
	if cfg.Agent.ToolResultStrategy != constants.ToolResultStrategySpill {
		t.Fatalf("ToolResultStrategy default = %q", cfg.Agent.ToolResultStrategy)
	}
	spill := filepath.Join(InvokrHomeDir(), constants.SpillDirName)
	if !slices.Contains(cfg.Workspace.ExtraRoots, spill) {
		t.Fatalf("workspace extra roots missing spill dir %q: %v", spill, cfg.Workspace.ExtraRoots)
	}
}

func TestToolResultMaxTokensEnvOverride(t *testing.T) {
	t.Setenv("INVOKR_TOOL_RESULT_MAX_TOKENS", "9000")
	cfg := configFromEnv()
	if cfg.Agent.ToolResultMaxTokens != 9000 {
		t.Fatalf("env override ignored: %d", cfg.Agent.ToolResultMaxTokens)
	}
}

// TestNativeToolCallingFollowsNativeRequests covers #446: the prompt contract
// and the request's tools array are one decision, so the flag is derived.
func TestNativeToolCallingFollowsNativeRequests(t *testing.T) {
	if cfg := (AgentConfig{}).withDefaults(); !cfg.NativeToolCalling {
		t.Fatalf("NativeToolCalling = false by default, want the native contract whenever native tools are sent")
	}
	if cfg := (AgentConfig{NativeToolRequestsDisabled: true}).withDefaults(); cfg.NativeToolCalling {
		t.Fatalf("NativeToolCalling = true while native tool requests are disabled")
	}
}
