package config

import (
	"cmp"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/constants"
)

type AppConfig struct {
	// Profile names the file profile `invokr config use` persisted;
	// --profile overrides it for one run.
	Profile         string                `json:"profile,omitempty"`
	SessionMode     string                `json:"session_mode,omitempty"`
	Provider        ProviderConfig        `json:"provider"`
	Agent           AgentConfig           `json:"agent,omitempty"`
	Log             LoggingConfig         `json:"log,omitempty"`
	Memory          MemoryConfig          `json:"memory,omitempty"`
	Workspace       WorkspaceConfig       `json:"workspace,omitempty"`
	Toolbox         ToolboxConfig         `json:"toolbox,omitempty"`
	Tools           ToolsConfig           `json:"tools,omitempty"`
	Skills          SkillsConfig          `json:"skills,omitempty"`
	Agents          AgentsConfig          `json:"agents,omitempty"`
	Shell           ShellConfig           `json:"shell,omitempty"`
	WebSearch       WebSearchConfig       `json:"web_search,omitempty"`
	CodeInterpreter CodeInterpreterConfig `json:"code_interpreter,omitempty"`
	MCP             MCPConfig             `json:"mcp,omitempty"`
	Sandbox         SandboxConfig         `json:"sandbox,omitempty"`
	Messaging       MessagingConfig       `json:"messaging,omitempty"`
	Approvals       ApprovalsConfig       `json:"approvals,omitempty"`
	LSP             LSPConfig             `json:"lsp,omitempty"`
	UI              UIConfig              `json:"ui,omitempty"`
}

// ToolsConfig narrows the tool set advertised to the model. Small models lose
// accuracy when a large tool list dilutes selection (bench: 7/10 toolcall
// failures were the model picking a distracting native tool), so a run can name
// the only tools it should see. Tools are still registered and loaded as usual;
// only advertisement and dispatch are restricted.
type ToolsConfig struct {
	// Allowlist names the tools advertised to the model. Empty means every
	// registered tool is advertised — the default. It is also the single
	// override in the other direction: naming apply_patch here promotes it
	// from tool_search-deferred to advertised.
	Allowlist    []string `json:"allowlist,omitempty" env:"INVOKR_TOOLS"`
	TodoDisabled bool     `json:"todo_disabled,omitempty" env:"INVOKR_TODO_DISABLED"`
	// Core adds optional tools (e.g. schedule, delegate_task) to the protected
	// core: their schemas ship natively from turn one, callable directly.
	Core []string `json:"core,omitempty" env:"INVOKR_CORE_TOOLS"`
	// TmuxHostServer points the tmux tool at the user's own tmux server,
	// unsandboxed, so the agent can drive the user's other sessions.
	TmuxHostServer bool `json:"tmux_host_server,omitempty" env:"INVOKR_TMUX_HOST_SERVER"`
}

// LSPConfig configures the language servers backing the `lsp` tool. Servers
// are never installed by invokr, and an entry whose command is missing from
// PATH is skipped, so the zero value is a working "use whatever is here".
type LSPConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
	// Servers replaces the built-in table when non-empty.
	Servers []LSPServerConfig `json:"servers,omitempty"`
}

type LSPServerConfig struct {
	Command    string   `json:"command"`
	Args       []string `json:"args,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
	LanguageID string   `json:"language_id,omitempty"`
}

func (c LSPConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

func (c LSPConfig) withEnv() LSPConfig {
	if value := Env("INVOKR_LSP_ENABLED"); value != "" {
		c.Enabled = boolPtr(envBool("INVOKR_LSP_ENABLED", c.IsEnabled()))
	}
	return c
}

// UIConfig holds appearance settings the terminal UI reads at startup.
type UIConfig struct {
	// SyntaxTheme names the chroma stylesheet fenced code is highlighted with.
	// Empty picks one to match the terminal background. The names are chroma's
	// own; markdown.SyntaxThemes lists them, and an unknown one falls back to
	// the default rather than failing.
	SyntaxTheme string `json:"syntax_theme,omitempty" env:"INVOKR_SYNTAX_THEME"`
	// Notify is the TUI attention-signal mode: "off" (default), "bell", or
	// "desktop" (bell + OSC 9 notification + window-title marker). The /notify
	// command's saved choice shadows it.
	Notify string `json:"notify,omitempty" env:"INVOKR_NOTIFY"`
}

// ApprovalsConfig configures standing approval grants (persistent, scoped,
// expiring auto-approvals). See capability/approval/standing.go.
type ApprovalsConfig struct {
	StandingGrantsDisabled bool   `json:"standing_grants_disabled,omitempty"`
	GrantsFile             string `json:"grants_file,omitempty"`
}

type ProviderConfig struct {
	BaseURL             string        `json:"base_url,omitempty" env:"INVOKR_BASE_URL"`
	APIKey              string        `json:"api_key,omitempty" env:"OPENAI_API_KEY,INVOKR_PROVIDER_API_KEY"`
	Model               string        `json:"model,omitempty" env:"INVOKR_MODEL"`
	ContextWindowTokens int           `json:"context_window_tokens,omitempty" env:"INVOKR_CONTEXT_WINDOW_TOKENS"`
	HTTPTimeout         time.Duration `json:"http_timeout,omitempty" env:"INVOKR_PROVIDER_HTTP_TIMEOUT"`
	StallTimeout        time.Duration `json:"stall_timeout,omitempty" env:"INVOKR_PROVIDER_STALL_TIMEOUT"`
	MaxRetries          int           `json:"max_retries,omitempty" env:"INVOKR_PROVIDER_MAX_RETRIES"`
	// Pricing is opt-in: Invokr ships no bundled price table (they rot, and
	// the primary target is local models that cost nothing). Cost accounting
	// is inert until the user supplies rates here.
	Pricing PricingConfig `json:"pricing,omitempty"`
	// PromptCache carries the optional explicit cache markers some hosted
	// providers require; automatic-caching providers need nothing here.
	PromptCache PromptCacheConfig `json:"prompt_cache,omitempty"`
	// Router is the optional multi-provider table. When it defines profiles,
	// requests route through them and BaseURL/APIKey/Model are ignored; when
	// it is empty, the single-provider fields above describe one
	// openai-compatible provider, which is the local llama.cpp setup.
	Router RouterConfig `json:"router,omitempty"`
}

// RouterConfig is a table of provider definitions plus the named profiles that
// select one, so switching providers is a config edit rather than a rebuild.
// Providers other than "openai-compatible" run as separate binaries the router
// launches, which is why no provider SDK is compiled into invokr.
type RouterConfig struct {
	// Active names the profile requests route to. Empty selects the sole
	// profile when exactly one is defined.
	Active    string                          `json:"active,omitempty"`
	Providers map[string]RouterProviderConfig `json:"providers,omitempty"`
	Profiles  map[string]RouterProfileConfig  `json:"profiles,omitempty"`
}

func (c RouterConfig) Enabled() bool { return len(c.Profiles) > 0 }

// ProfileIDs lists the defined profiles in a stable order, for pickers and
// diagnostics.
func (c RouterConfig) ProfileIDs() []string { return slices.Sorted(maps.Keys(c.Profiles)) }

// RouterProviderConfig is one provider definition.
type RouterProviderConfig struct {
	// Type selects the driver: "openai-compatible" is served in-process,
	// anything else ("anthropic", "openai", "bedrock") is a provider process.
	Type string `json:"type"`
	// BaseURL is the provider root, for openai-compatible providers.
	BaseURL string `json:"base_url,omitempty"`
	// APIKeyEnv names the environment variable holding the credential, which
	// keeps the key out of the config file. It takes precedence over APIKey.
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// APIKey stores the credential directly, as provider.api_key already does
	// for the single-provider path. Convenient, but it puts a secret in a file
	// on disk; prefer APIKeyEnv where the environment can carry it.
	APIKey string `json:"api_key,omitempty"`
	// Path pins an explicit provider-process binary; empty falls back to a
	// PATH lookup for go-inference-router-provider-<type>.
	Path    string         `json:"path,omitempty"`
	Version string         `json:"version,omitempty"`
	Options map[string]any `json:"options,omitempty"`
}

// RouterProfileConfig binds a model to a provider.
type RouterProfileConfig struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// PricingConfig holds user-supplied per-million-token rates for the configured
// provider/model, in whatever currency the user thinks in.
type PricingConfig struct {
	PromptPer1M     float64 `json:"prompt_per_1m,omitempty" env:"INVOKR_PRICING_PROMPT_PER_1M"`
	CompletionPer1M float64 `json:"completion_per_1m,omitempty" env:"INVOKR_PRICING_COMPLETION_PER_1M"`
	// CachedPromptPer1M is the discounted rate for prompt tokens the provider
	// served from its cache. Unset falls back to PromptPer1M, so a config that
	// predates prompt caching prices exactly as it did before.
	CachedPromptPer1M float64 `json:"cached_prompt_per_1m,omitempty" env:"INVOKR_PRICING_CACHED_PROMPT_PER_1M"`
}

// PromptCacheConfig gates provider-specific prompt-cache markers. Off by
// default: providers that cache automatically (OpenAI, DeepSeek) need no
// request fields, and sending unknown ones can get a request rejected.
type PromptCacheConfig struct {
	Enabled bool `json:"enabled,omitempty" env:"INVOKR_PROMPT_CACHE"`
	// RequestFields are merged into the chat-completions body at the top
	// level. Empty defaults to llama.cpp/openai-compat {"cache_prompt": true}.
	RequestFields map[string]any `json:"request_fields,omitempty"`
}

// RequestExtra returns the fields to merge into the provider request, or nil
// when explicit markers are disabled.
func (c PromptCacheConfig) RequestExtra() map[string]any {
	if !c.Enabled {
		return nil
	}
	if len(c.RequestFields) > 0 {
		return c.RequestFields
	}
	return map[string]any{"cache_prompt": true}
}

// Configured reports whether any rate was supplied. CachedPromptPer1M is
// deliberately not counted: it is a modifier on the prompt rate (Cost falls
// back to PromptPer1M when it is unset), never a basis for pricing a session on
// its own, so a config setting only the cached rate stays "unpriced" rather
// than letting max_session_cost enforce against a figure with a zero prompt and
// completion rate in it.
func (p PricingConfig) Configured() bool { return p.PromptPer1M > 0 || p.CompletionPer1M > 0 }

// Cost prices one session's tokens. cachedPromptTokens is the cache-served
// subset of promptTokens (OpenAI reports it that way) and is billed at
// CachedPromptPer1M, falling back to the full prompt rate when that is unset.
func (p PricingConfig) Cost(promptTokens, cachedPromptTokens, completionTokens int64) float64 {
	cached := min(max(cachedPromptTokens, 0), promptTokens)
	return (float64(promptTokens-cached)*p.PromptPer1M + float64(cached)*cmp.Or(p.CachedPromptPer1M, p.PromptPer1M) + float64(completionTokens)*p.CompletionPer1M) / 1e6
}

type AgentConfig struct {
	ModelOptions map[string]map[string]string `json:"model_options,omitempty"`
	SystemPrompt string                       `json:"system_prompt,omitempty" env:"INVOKR_SYSTEM_PROMPT"`
	// PromptSource records where SystemPrompt came from ("default", "config",
	// "persona", "env"). Derived by the loader, never read from config JSON;
	// it exists so a recorded run says which base prompt was in play.
	PromptSource    string  `json:"-"`
	SummarizerModel string  `json:"summarizer_model,omitempty" env:"INVOKR_SUMMARIZER_MODEL"`
	Temperature     float64 `json:"temperature,omitempty" env:"INVOKR_TEMPERATURE"`
	// TemperatureDisabled omits the sampling temperature from requests. The
	// newest Anthropic models removed the parameter and reject a request that
	// carries it, so a provider profile pointing at one needs this set.
	TemperatureDisabled bool `json:"temperature_disabled,omitempty" env:"INVOKR_TEMPERATURE_DISABLED"`
	NPredict            int  `json:"n_predict,omitempty" env:"INVOKR_N_PREDICT"`
	// ReasoningEffort is llama.cpp's reasoning_effort level (none|minimal|low|medium|high|xhigh|max); empty omits it.
	ReasoningEffort string `json:"reasoning_effort,omitempty" env:"INVOKR_REASONING_EFFORT"`
	MaxTurns        int    `json:"max_turns,omitempty" env:"INVOKR_AGENT_MAX_TURNS"`
	// ReadTurnWeight is what a turn whose every tool call was read-only costs
	// against MaxTurns (0..1); a turn every call of which was denied costs
	// nothing, and every other turn costs one. 1 restores the static count. 0
	// means "unset" and takes the default, since WithDefaults fills it in with
	// cmp.Or — a free read turn is not expressible.
	ReadTurnWeight float64 `json:"read_turn_weight,omitempty" env:"INVOKR_READ_TURN_WEIGHT"`
	// HardTurnMultiplier bounds the raw loop iterations at MaxTurns * this
	// (>=1), whatever the weighted budget and its extensions say. It is the
	// backstop that keeps zero-cost turns from spinning forever.
	HardTurnMultiplier float64 `json:"hard_turn_multiplier,omitempty" env:"INVOKR_HARD_TURN_MULTIPLIER"`
	// TurnExtension is how many turns are added when the budget runs out and
	// the run is still making progress over the last TurnExtension turns — a
	// new tool call or a landed edit, and no repeat nudge — capped by
	// HardTurnMultiplier all the same. Negative disables the extension.
	TurnExtension int `json:"turn_extension,omitempty" env:"INVOKR_TURN_EXTENSION"`
	// ToolQuotas caps how many times one run may call a tool; a call past the
	// quota is refused with a flagged result the model still sees, rather than
	// run. 0 or less is unlimited, and unnamed tools are unlimited. Set from
	// the environment as "web_search=5,web_fetch=8".
	ToolQuotas map[string]int `json:"tool_quotas,omitempty" env:"INVOKR_TOOL_QUOTAS"`
	// RunTimeout is a wall-clock budget for one request; 0 disables it. Past
	// ~75% the model is told to finish; at the budget any in-flight
	// completion is cut and the run ends through the same best-effort wrap-up
	// as the turn ceiling, which gets a further 25% of the budget. A harness
	// deadline at ~1.3x RunTimeout therefore sees an answer and files on
	// disk instead of a run cut off mid-generation.
	RunTimeout time.Duration `json:"run_timeout,omitempty" env:"INVOKR_AGENT_RUN_TIMEOUT"`
	// NativeToolCalling selects the native tool contract for the system
	// prompt: tool definitions ride the request's tools array, and the prompt
	// carries only the behavioural rules. It is derived in withDefaults from
	// NativeToolRequestsDisabled — sending native tool definitions and the
	// tagged-JSON listing at once is two contradictory contracts, not a
	// choice (#446) — so setting it alone has no effect.
	NativeToolCalling          bool `json:"native_tool_calling,omitempty" env:"INVOKR_NATIVE_TOOL_CALLING"`
	NativeToolRequestsDisabled bool `json:"native_tool_requests_disabled,omitempty" env:"INVOKR_NATIVE_TOOL_REQUESTS_DISABLED"`
	// ParallelToolCalls is derived from ParallelToolCallsDisabled in
	// withEnv/withDefaults, never read from config JSON: a bool can't tell
	// an explicit "false" from an unset field, so a user-supplied
	// parallel_tool_calls would be silently overwritten. The only knob is
	// parallel_tool_calls_disabled.
	ParallelToolCalls         bool `json:"-"`
	ParallelToolCallsDisabled bool `json:"parallel_tool_calls_disabled,omitempty" env:"INVOKR_PARALLEL_TOOL_CALLS_DISABLED"`
	ContextWarningPercent     int  `json:"context_warning_percent,omitempty" env:"INVOKR_CONTEXT_WARNING_PERCENT"`
	// Zero uses a window-based threshold; set 80 to opt into the old fixed trigger.
	ContextCompactPercent int `json:"context_compact_percent,omitempty" env:"INVOKR_CONTEXT_COMPACT_PERCENT"`
	// MaxSessionTokens caps total session tokens (prompt + completion) as
	// reported by the provider; 0 disables the cap. MaxSessionCost caps
	// session spend and requires provider.pricing to be configured.
	MaxSessionTokens int64   `json:"max_session_tokens,omitempty" env:"INVOKR_AGENT_MAX_SESSION_TOKENS"`
	MaxSessionCost   float64 `json:"max_session_cost,omitempty" env:"INVOKR_AGENT_MAX_SESSION_COST"`
	// MaxRequestTokens caps one request's token spend; 0 disables the cap.
	// A turn is charged its completion plus the growth of its prompt, not the
	// whole prompt: the transcript prefix is re-sent every turn and charging
	// it again spent the budget on work that was never redone (#445). When
	// the provider reports no usage the local len/4 estimate is charged
	// instead, prompt included, so the backstop still holds everywhere.
	// Compaction/summarizer spend is not charged here — it belongs to the
	// session caps, and charging it ended long runs early (#496).
	MaxRequestTokens int64 `json:"max_request_tokens,omitempty" env:"INVOKR_AGENT_MAX_REQUEST_TOKENS"`
	// ToolResultMaxTokens is the per-result token budget at the dispatch gate;
	// a result estimated over it is reduced by the oversize strategy for its
	// tool instead of entering the context whole.
	ToolResultMaxTokens int `json:"tool_result_max_tokens,omitempty" env:"INVOKR_TOOL_RESULT_MAX_TOKENS"`
	// ToolResultStrategy names the default oversize strategy;
	// ToolResultStrategies overrides it per tool name.
	ToolResultStrategy   string            `json:"tool_result_strategy,omitempty" env:"INVOKR_TOOL_RESULT_STRATEGY"`
	ToolResultStrategies map[string]string `json:"tool_result_strategies,omitempty"`
}

type MemoryConfig struct {
	Dir string `json:"dir,omitempty" env:"INVOKR_MEMORY_DIR"`
	// ProjectDir overrides where the project MEMORY.md lives. Empty scopes it to a
	// per-workspace directory under Dir; pointing it back at Dir (e.g.
	// ~/.invokr) restores the old global behaviour.
	ProjectDir string             `json:"project_dir,omitempty" env:"INVOKR_MEMORY_PROJECT_DIR"`
	Zerfoo     ZerfooMemoryConfig `json:"zerfoo,omitempty"`
	RecallGate RecallGateConfig   `json:"recall_gate,omitempty"`
	Forgetting ForgettingConfig   `json:"forgetting,omitempty"`
	// DisableSessionTitles turns off the one short model call made at shutdown
	// to label a session for the --resume picker. Zero value keeps titles on;
	// set it when that per-run cost is unwanted (or to keep a scripted provider
	// from seeing the extra call).
	DisableSessionTitles bool `json:"disable_session_titles,omitempty" env:"INVOKR_MEMORY_DISABLE_SESSION_TITLES"`
}

// RecallGateConfig configures the optional nothink recall gate: a cheap,
// thinking-suppressed probe completion issued at session start that decides
// whether long-term memory recall would help and, if so, emits targeted recall
// queries injected as a "Relevant Memory" block. Off by default. See
// docs/private/plans/nothink-recall-gate.md.
type RecallGateConfig struct {
	Enabled         bool   `json:"enabled,omitempty" env:"INVOKR_MEMORY_RECALL_GATE_ENABLED"`
	Model           string `json:"model,omitempty" env:"INVOKR_MEMORY_RECALL_GATE_MODEL"`
	NothinkSuffix   string `json:"nothink_suffix,omitempty" env:"INVOKR_MEMORY_RECALL_GATE_NOTHINK_SUFFIX"`
	MaxQueries      int    `json:"max_queries,omitempty" env:"INVOKR_MEMORY_RECALL_GATE_MAX_QUERIES"`
	PerQueryLimit   int    `json:"per_query_limit,omitempty" env:"INVOKR_MEMORY_RECALL_GATE_PER_QUERY_LIMIT"`
	MaxHits         int    `json:"max_hits,omitempty" env:"INVOKR_MEMORY_RECALL_GATE_MAX_HITS"`
	BudgetChars     int    `json:"budget_chars,omitempty" env:"INVOKR_MEMORY_RECALL_GATE_BUDGET_CHARS"`
	MinMessageChars int    `json:"min_message_chars,omitempty" env:"INVOKR_MEMORY_RECALL_GATE_MIN_MESSAGE_CHARS"`
	TimeoutMS       int    `json:"timeout_ms,omitempty" env:"INVOKR_MEMORY_RECALL_GATE_TIMEOUT_MS"`
}

// ForgettingConfig configures Invokr's opt-in memory decay: an Ebbinghaus-style
// retention model where runtime SQLite records fade over time, are strengthened
// on recall, and drop into a cold archive when retention falls below a
// threshold. Disabled by default — when off, memory behavior is byte-for-byte
// identical to the pre-forgetting store. See docs/memory.md.
type ForgettingConfig struct {
	Enabled               bool     `json:"enabled,omitempty" env:"INVOKR_MEMORY_FORGETTING_ENABLED"`
	HalfLifeDays          float64  `json:"half_life_days,omitempty" env:"INVOKR_MEMORY_FORGETTING_HALF_LIFE_DAYS"`
	MinRetention          float64  `json:"min_retention,omitempty" env:"INVOKR_MEMORY_FORGETTING_MIN_RETENTION"`
	ReinforcementFactor   float64  `json:"reinforcement_factor,omitempty" env:"INVOKR_MEMORY_FORGETTING_REINFORCEMENT_FACTOR"`
	MaxStrength           float64  `json:"max_strength,omitempty" env:"INVOKR_MEMORY_FORGETTING_MAX_STRENGTH"`
	PurgeAfterDays        int      `json:"purge_after_days,omitempty" env:"INVOKR_MEMORY_FORGETTING_PURGE_AFTER_DAYS"`
	RawEventRetentionDays int      `json:"raw_event_retention_days,omitempty" env:"INVOKR_MEMORY_FORGETTING_RAW_EVENT_RETENTION_DAYS"`
	ExemptKinds           []string `json:"exempt_kinds,omitempty" env:"INVOKR_MEMORY_FORGETTING_EXEMPT_KINDS"`
}

// Default forgetting parameters. See docs/private/plans/memory-forgetting.md for the
// rationale behind each value.
// "doc" is exempt because doc records mirror workspace files (ADR 0015):
// their lifecycle follows the file, not recall frequency.
var DefaultForgettingExemptKinds = []string{"preference", "profile", "doc"}

const (
	DefaultForgettingHalfLifeDays          = 14.0
	DefaultForgettingMinRetention          = 0.15
	DefaultForgettingReinforcementFactor   = 1.5
	DefaultForgettingMaxStrength           = 10.0
	DefaultForgettingRawEventRetentionDays = 7
)

// ZerfooMemoryConfig configures Invokr's built-in Go model runtime
// (github.com/zerfoo/zerfoo) used for memory embeddings, summarization, and
// vector recall. It replaces the former llama.cpp / llama-server runtime: no
// sidecar process, CGo, or Python is involved.
//
// Models may be given as HuggingFace IDs (downloaded and cached under
// ~/.cache/zerfoo/models) or, for offline use, as local GGUF paths via the
// *Path fields, which take precedence over the model IDs.
type ZerfooMemoryConfig struct {
	Enabled                   bool    `json:"enabled,omitempty" env:"INVOKR_MEMORY_ZERFOO_ENABLED"`
	EmbeddingModel            string  `json:"embedding_model,omitempty" env:"INVOKR_MEMORY_ZERFOO_EMBEDDING_MODEL"`
	SummarizationModel        string  `json:"summarization_model,omitempty" env:"INVOKR_MEMORY_ZERFOO_SUMMARIZATION_MODEL"`
	EmbeddingPath             string  `json:"embedding_path,omitempty" env:"INVOKR_MEMORY_ZERFOO_EMBEDDING_PATH"`
	SummarizationPath         string  `json:"summarization_path,omitempty" env:"INVOKR_MEMORY_ZERFOO_SUMMARIZATION_PATH"`
	EmbeddingDimensions       int     `json:"embedding_dimensions,omitempty" env:"INVOKR_MEMORY_ZERFOO_EMBEDDING_DIMENSIONS"`
	MaxSummaryTokens          int     `json:"max_summary_tokens,omitempty" env:"INVOKR_MEMORY_ZERFOO_MAX_SUMMARY_TOKENS"`
	MaxRawEventsBeforeSummary int     `json:"max_raw_events_before_summary,omitempty" env:"INVOKR_MEMORY_ZERFOO_MAX_RAW_EVENTS_BEFORE_SUMMARY"`
	MinImportanceToStore      float64 `json:"min_importance_to_store,omitempty" env:"INVOKR_MEMORY_ZERFOO_MIN_IMPORTANCE_TO_STORE"`
	RequestTimeoutMS          int     `json:"request_timeout_ms,omitempty" env:"INVOKR_MEMORY_ZERFOO_REQUEST_TIMEOUT_MS"`
}

// Default Zerfoo memory models. See docs/memory.md for the model selection
// rationale (LFM2.5-Embedding-350M for embeddings, LFM2-1.2B-RAG for
// summarization).
const (
	// Qwen2.5 (qwen2 arch) GGUFs load in zerfoo's in-process loader; zerfoo
	// v1.55.0 does NOT support the qwen3 arch (e.g. Qwen3-Embedding), so stick to
	// qwen2/llama-family models. zerfoo's Embed mean-pools the static
	// token-embedding table, so EmbeddingDims must equal the embedding model's
	// hidden size (Qwen2.5-0.5B = 896). The /Q8_0 and /Q4_K_M suffixes select the
	// quant. Re-evaluate candidates with scripts/eval-memory-model.sh.
	DefaultZerfooEmbeddingModel     = "Qwen/Qwen2.5-0.5B-Instruct-GGUF/Q8_0"
	DefaultZerfooSummarizationModel = "Qwen/Qwen2.5-1.5B-Instruct-GGUF/Q4_K_M"
	DefaultZerfooEmbeddingDims      = 896
)

// EmbeddingRef returns the local path if set, otherwise the model ID.
func (c ZerfooMemoryConfig) EmbeddingRef() string {
	return cmp.Or(strings.TrimSpace(c.EmbeddingPath), strings.TrimSpace(c.EmbeddingModel))
}

// SummarizationRef returns the local path if set, otherwise the model ID.
func (c ZerfooMemoryConfig) SummarizationRef() string {
	return cmp.Or(strings.TrimSpace(c.SummarizationPath), strings.TrimSpace(c.SummarizationModel))
}

type WorkspaceConfig struct {
	Root       string   `json:"root,omitempty" env:"INVOKR_WORKSPACE_ROOT"`
	ExtraRoots []string `json:"extra_roots,omitempty" env:"INVOKR_WORKSPACE_EXTRA_ROOTS"`
	// ReadonlyRoots are readable like extra roots but refuse every write:
	// reference checkouts the agent may consult and must not modify.
	ReadonlyRoots    []string `json:"readonly_roots,omitempty" env:"INVOKR_WORKSPACE_READONLY_ROOTS"`
	AllowHiddenPaths bool     `json:"allow_hidden_paths,omitempty" env:"INVOKR_WORKSPACE_ALLOW_HIDDEN_PATHS"`
}

type ToolboxConfig struct {
	Dir       string `json:"dir,omitempty" env:"INVOKR_TOOLBOX_DIR"`
	GoBin     string `json:"go_bin,omitempty" env:"INVOKR_GO_BIN"`
	NodeBin   string `json:"node_bin,omitempty" env:"INVOKR_NODE_BIN"`
	PythonBin string `json:"python_bin,omitempty" env:"INVOKR_PYTHON_BIN"`
	VenvDir   string `json:"venv_dir,omitempty" env:"INVOKR_TOOLBOX_VENV_DIR"`
	// IsolateEnv controls whether toolbox tools inherit the full host
	// process environment (which leaks secrets like OPENAI_API_KEY). Nil
	// defaults to true (isolated); set explicitly false to inherit.
	IsolateEnv *bool             `json:"isolate_env,omitempty"`
	Env        map[string]string `json:"env,omitempty" env:"INVOKR_TOOLBOX_ENV"`

	GoCacheDir string `json:"go_cache_dir,omitempty" env:"INVOKR_GO_CACHE_DIR"`
	// PythonPipArgs is whitespace-separated in the environment, unlike the
	// comma-separated lists the env tag walker parses, so it stays hand-written.
	PythonPipArgs  []string `json:"python_pip_args,omitempty"`
	MaxOutputBytes int      `json:"max_output_bytes,omitempty" env:"INVOKR_TOOLBOX_MAX_OUTPUT_BYTES"`

	BrowserAgentState string `json:"browser_agent_state,omitempty" env:"INVOKR_BROWSER_AGENT_STATE"`
	// TokfDisabled is driven by an inversion pair (INVOKR_TOKF_DISABLED and
	// INVOKR_TOKF_ENABLED), so it stays hand-written.
	TokfDisabled bool   `json:"tokf_disabled,omitempty"`
	TokfBin      string `json:"tokf_bin,omitempty" env:"INVOKR_TOKF_BIN"`
	TokfHome     string `json:"tokf_home,omitempty" env:"INVOKR_TOKF_HOME"`

	// AllowUnsandboxed opts out of namespace sandboxing for toolbox tools on
	// platforms/environments where it is unavailable. Off by default: when
	// sandboxing is unavailable and this is false, toolbox execution is
	// refused rather than silently running unconfined on the host.
	AllowUnsandboxed bool `json:"allow_unsandboxed,omitempty" env:"INVOKR_TOOLBOX_ALLOW_UNSANDBOXED"`
	// MemoryLimitBytes bounds a toolbox tool process via RLIMIT_AS. Zero
	// means no limit is applied.
	MemoryLimitBytes int64 `json:"memory_limit_bytes,omitempty" env:"INVOKR_TOOLBOX_MEMORY_LIMIT_BYTES"`
	// ExecTimeoutMS bounds toolbox tool execution wall-clock time; it is the
	// backstop against runaway processes (e.g. fork bombs) that resource
	// limits alone don't reliably contain.
	ExecTimeoutMS int `json:"exec_timeout_ms,omitempty" env:"INVOKR_TOOLBOX_EXEC_TIMEOUT_MS"`

	// MaxLimit* cap what a plugin manifest's `limits` block may ask for. A
	// manifest value above its cap is refused at load with a diagnostic rather
	// than clamped, so a tool never runs under limits it did not declare.
	MaxLimitTimeoutMS   int   `json:"max_limit_timeout_ms,omitempty" env:"INVOKR_TOOLBOX_MAX_LIMIT_TIMEOUT_MS"`
	MaxLimitMemoryBytes int64 `json:"max_limit_memory_bytes,omitempty" env:"INVOKR_TOOLBOX_MAX_LIMIT_MEMORY_BYTES"`
	MaxLimitOutputBytes int   `json:"max_limit_output_bytes,omitempty" env:"INVOKR_TOOLBOX_MAX_LIMIT_OUTPUT_BYTES"`

	// Package policy covers the resolved graph and cached environments, using
	// Python name normalization. Deny wins; nonempty Allow is default-deny.
	// Restricted policies require per-tool venvs (VenvDir must be unset).
	PackageAllow         []string `json:"package_allow,omitempty" env:"INVOKR_TOOLBOX_PKG_ALLOW"`
	PackageDeny          []string `json:"package_deny,omitempty" env:"INVOKR_TOOLBOX_PKG_DENY"`
	PackageIndexURL      string   `json:"package_index_url,omitempty" env:"INVOKR_TOOLBOX_PACKAGE_INDEX_URL"`
	PackageMetadataURL   string   `json:"package_metadata_url,omitempty" env:"INVOKR_TOOLBOX_PACKAGE_METADATA_URL"`
	PackageIndexHosts    []string `json:"package_index_hosts,omitempty" env:"INVOKR_TOOLBOX_PACKAGE_INDEX_HOSTS"`
	PackageArtifactHosts []string `json:"package_artifact_hosts,omitempty" env:"INVOKR_TOOLBOX_PACKAGE_ARTIFACT_HOSTS"`
	ReleaseCooldownDays  int      `json:"release_cooldown_days,omitempty" env:"INVOKR_TOOLBOX_RELEASE_COOLDOWN_DAYS"`
}

type SkillsConfig struct {
	Dir string `json:"dir,omitempty" env:"INVOKR_SKILLS_DIR"`
	// Hubs lists skill hubs — git repositories (GitHub "owner/repo" shorthand
	// or full clone URLs) whose skills/<name>/SKILL.md directories are
	// installable via `invokr skills install`.
	Hubs []string `json:"hubs,omitempty" env:"INVOKR_SKILLS_HUBS"`
	// HubCacheDir is where hub repositories are shallow-cloned.
	HubCacheDir string `json:"hub_cache_dir,omitempty" env:"INVOKR_SKILLS_HUB_CACHE_DIR"`
}

// AgentsConfig configures the directory of custom subagent definition files.
type AgentsConfig struct {
	Dir string `json:"dir,omitempty"`
}

type ShellConfig struct {
	Enabled              *bool    `json:"enabled,omitempty"`
	AllowedCommands      []string `json:"allowed_commands,omitempty" env:"INVOKR_SHELL_ALLOWED_COMMANDS"`
	AllowWriteCommands   bool     `json:"allow_write_commands,omitempty" env:"INVOKR_SHELL_ALLOW_WRITE_COMMANDS"`
	AllowNetworkCommands bool     `json:"allow_network_commands,omitempty" env:"INVOKR_SHELL_ALLOW_NETWORK_COMMANDS"`
	// AllowAnyCommand lifts the executable allowlist: a root that is not on it
	// is classified as write tier and asked for, instead of being refused
	// outright. It is the policy half of `exec --allow-all`, whose whole point
	// is unattended arbitrary execution; the allowlist is a separate gate and
	// used to survive it, leaving headless runs unable to touch npm, make or
	// any other project driver.
	AllowAnyCommand bool `json:"allow_any_command,omitempty" env:"INVOKR_SHELL_ALLOW_ANY_COMMAND"`
	// IsolateEnv controls whether shell commands inherit the full host process
	// environment (which leaks secrets like OPENAI_API_KEY). Nil defaults to
	// true (isolated); set explicitly false to inherit the host env.
	IsolateEnv *bool `json:"isolate_env,omitempty"`
	// EnvPassthrough names extra host env vars to expose to shell commands when
	// isolating (on top of the always-forwarded PATH/HOME/LANG/TERM/TMPDIR).
	EnvPassthrough []string `json:"env_passthrough,omitempty" env:"INVOKR_SHELL_ENV_PASSTHROUGH"`
}

type WebSearchConfig struct {
	// Disabled leaves web_search unregistered. It is the opt-out for the
	// default searxng provider, which shells out to a container engine and
	// starts a long-lived container on first search; naming a provider you
	// have no key for is not an opt-out, since the tool is then not offered
	// for the wrong reason.
	Disabled bool `json:"disabled,omitempty" env:"INVOKR_WEB_SEARCH_DISABLED"`
	// Providers names the providers to query. A searxng instance already on
	// PATH is used as a fallback even when it is not listed here, so a search
	// still answers when the listed providers have no key.
	Providers      []string               `json:"providers,omitempty" env:"INVOKR_WEB_SEARCH_PROVIDERS"`
	CustomHTTP     CustomHTTPSearchConfig `json:"custom_http,omitempty"`
	SearXNG        SearXNGSearchConfig    `json:"searxng,omitempty"`
	CacheDir       string                 `json:"cache_dir,omitempty" env:"INVOKR_WEB_SEARCH_CACHE_DIR"`
	AllowSensitive bool                   `json:"allow_sensitive,omitempty" env:"INVOKR_WEB_SEARCH_ALLOW_SENSITIVE"`
	SensitiveTerms []string               `json:"sensitive_terms,omitempty" env:"INVOKR_WEB_SEARCH_SENSITIVE_TERMS"`
	PrivateHosts   []string               `json:"private_hosts,omitempty" env:"INVOKR_WEB_SEARCH_PRIVATE_HOSTS"`
	// AllowPrivateHosts disables web_fetch's SSRF guard entirely: loopback,
	// RFC1918, CGNAT and link-local targets become dialable. Dangerous — off by
	// default; only hermetic tests and self-hosted setups should opt in. It does
	// not affect the PrivateHosts redaction list above.
	AllowPrivateHosts bool     `json:"allow_private_hosts,omitempty" env:"INVOKR_WEB_FETCH_ALLOW_PRIVATE_HOSTS"`
	PrivateRepoNames  []string `json:"private_repo_names,omitempty" env:"INVOKR_WEB_SEARCH_PRIVATE_REPO_NAMES"`
}

type CodeInterpreterConfig struct {
	Backend          string `json:"backend,omitempty" env:"INVOKR_CODE_INTERPRETER_BACKEND"`
	PythonBin        string `json:"python_bin,omitempty" env:"INVOKR_CODE_INTERPRETER_PYTHON_BIN"`
	SessionDir       string `json:"session_dir,omitempty" env:"INVOKR_CODE_INTERPRETER_SESSION_DIR"`
	DefaultTimeoutMS int    `json:"default_timeout_ms,omitempty" env:"INVOKR_CODE_INTERPRETER_DEFAULT_TIMEOUT_MS"`
	MaxTimeoutMS     int    `json:"max_timeout_ms,omitempty" env:"INVOKR_CODE_INTERPRETER_MAX_TIMEOUT_MS"`
	MaxOutputBytes   int    `json:"max_output_bytes,omitempty" env:"INVOKR_CODE_INTERPRETER_MAX_OUTPUT_BYTES"`
	MaxArtifactBytes int    `json:"max_artifact_bytes,omitempty" env:"INVOKR_CODE_INTERPRETER_MAX_ARTIFACT_BYTES"`
	MemoryLimitBytes int64  `json:"memory_limit_bytes,omitempty" env:"INVOKR_CODE_INTERPRETER_MEMORY_LIMIT_BYTES"`
	ContainerEngine  string `json:"container_engine,omitempty" env:"INVOKR_CODE_INTERPRETER_CONTAINER_ENGINE"`
	ContainerImage   string `json:"container_image,omitempty" env:"INVOKR_CODE_INTERPRETER_CONTAINER_IMAGE"`
	ContainerCPUs    string `json:"container_cpus,omitempty" env:"INVOKR_CODE_INTERPRETER_CONTAINER_CPUS"`
	ContainerNetwork bool   `json:"container_network,omitempty" env:"INVOKR_CODE_INTERPRETER_CONTAINER_NETWORK"`
	AllowEnv         bool   `json:"allow_env,omitempty" env:"INVOKR_CODE_INTERPRETER_ALLOW_ENV"`
}

// SandboxConfig controls the self-re-exec container sandbox. When Enabled is
// true (and INVOKR_IN_SANDBOX is unset), the process re-execs itself inside a
// docker container before Bootstrap runs. Auto-build is only supported on Linux
// hosts; on other platforms set Image to a pre-built linux/amd64 image.
type SandboxConfig struct {
	// Enabled activates the self-re-exec sandbox. When true and
	// INVOKR_IN_SANDBOX is unset, the process re-execs itself inside docker
	// before Bootstrap runs.
	Enabled bool `json:"enabled,omitempty" env:"INVOKR_SANDBOX"`
	// Image overrides the auto-built image tag. When non-empty, image build is
	// skipped entirely and this value is passed to docker run as-is.
	Image string `json:"image,omitempty" env:"INVOKR_SANDBOX_IMAGE"`
	// Engine is the container CLI binary name: "docker" (default) or "podman".
	Engine string `json:"engine,omitempty" env:"INVOKR_SANDBOX_ENGINE"`
}

type MCPConfig struct {
	Servers map[string]MCPServerConfig `json:"servers,omitempty"`
}

// MCPServerConfig describes one MCP server. Exactly one transport must be
// configured: Command (stdio subprocess) or URL (streamable HTTP). Credentials
// for HTTP servers are named by HeadersEnv (header name -> env var) and read
// from the environment at connect time, never stored in config plaintext.
type MCPServerConfig struct {
	Command                  string            `json:"command,omitempty"`
	Args                     []string          `json:"args,omitempty"`
	URL                      string            `json:"url,omitempty"`
	HeadersEnv               map[string]string `json:"headers_env,omitempty"`
	Enabled                  bool              `json:"enabled,omitempty"`
	Required                 bool              `json:"required,omitempty"`
	DefaultToolsApprovalMode string            `json:"default_tools_approval_mode,omitempty"`
	EnabledTools             []string          `json:"enabled_tools,omitempty"`
	DisabledTools            []string          `json:"disabled_tools,omitempty"`
}

type CustomHTTPSearchConfig struct {
	Endpoint   string            `json:"endpoint,omitempty" env:"INVOKR_WEB_SEARCH_CUSTOM_HTTP_ENDPOINT"`
	Method     string            `json:"method,omitempty" env:"INVOKR_WEB_SEARCH_CUSTOM_HTTP_METHOD"`
	HeadersEnv map[string]string `json:"headers_env,omitempty" env:"INVOKR_WEB_SEARCH_CUSTOM_HTTP_HEADERS_ENV"`
	QueryParam string            `json:"query_param,omitempty" env:"INVOKR_WEB_SEARCH_CUSTOM_HTTP_QUERY_PARAM"`
	Mappings   map[string]string `json:"mappings,omitempty" env:"INVOKR_WEB_SEARCH_CUSTOM_HTTP_MAPPINGS"`
}

// SearXNGSearchConfig backs the searxng web_search provider. With Endpoint
// empty, invokr starts (and reuses) a local container on first search; set
// Endpoint to point at an existing instance and skip docker entirely.
type SearXNGSearchConfig struct {
	Endpoint string `json:"endpoint,omitempty" env:"INVOKR_WEB_SEARCH_SEARXNG_ENDPOINT"`
	Image    string `json:"image,omitempty" env:"INVOKR_WEB_SEARCH_SEARXNG_IMAGE"`
	Port     int    `json:"port,omitempty" env:"INVOKR_WEB_SEARCH_SEARXNG_PORT"`
	Engine   string `json:"engine,omitempty" env:"INVOKR_WEB_SEARCH_SEARXNG_ENGINE"`
}

func DefaultAppConfigForHome(homeDir string) AppConfig { return AppConfig{}.withDefaults(homeDir) }

func (c AppConfig) WithDefaults() AppConfig { return c.withDefaults(defaultInvokrHomeDir()) }

func (c AppConfig) withDefaults(homeDir string) AppConfig {
	c.Provider = c.Provider.withDefaults()
	c.Agent = c.Agent.withDefaults()
	// The "minimal" preset expands into its tool list; any other allowlist is
	// taken as written.
	if len(c.Tools.Allowlist) == 1 && c.Tools.Allowlist[0] == constants.MinimalToolPreset {
		c.Tools.Allowlist = slices.Clone(constants.MinimalTools)
	}
	c.Memory = c.Memory.withDefaults(homeDir)
	c.Workspace = c.Workspace.withDefaults(homeDir)
	c.Toolbox = c.Toolbox.WithDefaults(homeDir)
	c.Skills.Dir = cmp.Or(c.Skills.Dir, filepath.Join(homeDir, constants.DefaultSkillsDir))
	c.Skills.Hubs = orSlice(c.Skills.Hubs, []string{constants.DefaultSkillsHub})
	c.Skills.HubCacheDir = cmp.Or(c.Skills.HubCacheDir, filepath.Join(homeDir, constants.DefaultHubsDir))
	c.Agents.Dir = cmp.Or(c.Agents.Dir, filepath.Join(homeDir, "agents"))
	c.Shell = c.Shell.withDefaults()
	c.WebSearch = c.WebSearch.withDefaults()
	c.CodeInterpreter = c.CodeInterpreter.withDefaults(homeDir)
	if c.MCP.Servers == nil {
		c.MCP.Servers = map[string]MCPServerConfig{}
	}
	c.Approvals.GrantsFile = cmp.Or(c.Approvals.GrantsFile, filepath.Join(homeDir, "grants.json"))
	c.Sandbox.Engine = cmp.Or(c.Sandbox.Engine, "docker")
	c.Messaging.Signal.Socket = cmp.Or(c.Messaging.Signal.Socket, "/var/run/signal-cli/socket")
	c.Log.Level = cmp.Or(c.Log.Level, DefaultLevel)
	c.Log.Format = cmp.Or(c.Log.Format, DefaultFormat)
	c.Log.Output = cmp.Or(c.Log.Output, DefaultOutput)
	return c
}

// withEnv applies the `env:` struct tags across the whole tree (see
// envtags.go), then the handful of cases tags cannot express: tri-state *bool
// fields, the tokf enable/disable inversion, the whitespace-separated pip args,
// and ParallelToolCalls's derivation.
func (c AppConfig) withEnv() AppConfig {
	applyEnv(&c)
	// Non-empty, not merely set: applyEnv ignores an empty value, so marking
	// the source "env" on INVOKR_SYSTEM_PROMPT="" would attribute the prompt
	// to an override that never happened.
	if Env("INVOKR_SYSTEM_PROMPT") != "" {
		c.Agent.PromptSource = "env"
	}
	c.Agent.ParallelToolCalls = !c.Agent.ParallelToolCallsDisabled
	c.Toolbox = c.Toolbox.withEnv()
	c.Shell = c.Shell.withEnv()
	c.LSP = c.LSP.withEnv()
	return c
}

// withEnv covers the ToolboxConfig cases the `env:` tags cannot express.
func (c ToolboxConfig) withEnv() ToolboxConfig {
	if value := Env("INVOKR_TOOLBOX_ISOLATE_ENV"); value != "" {
		c.IsolateEnv = boolPtr(envBool("INVOKR_TOOLBOX_ISOLATE_ENV", c.IsolateHostEnv()))
	}
	c.PythonPipArgs = envFields("INVOKR_PYTHON_PIP_ARGS", c.PythonPipArgs)
	c.TokfDisabled = envBool("INVOKR_TOKF_DISABLED", c.TokfDisabled)
	c.TokfDisabled = !envBool("INVOKR_TOKF_ENABLED", !c.TokfDisabled)
	return c
}

// withEnv covers the ShellConfig cases the `env:` tags cannot express.
func (c ShellConfig) withEnv() ShellConfig {
	if value := Env("INVOKR_SHELL_ISOLATE_ENV"); value != "" {
		c.IsolateEnv = boolPtr(envBool("INVOKR_SHELL_ISOLATE_ENV", c.IsolateHostEnv()))
	}
	if envBool("INVOKR_SHELL_DISABLED", false) {
		c.Enabled = boolPtr(false)
	}
	if value := Env("INVOKR_SHELL_ENABLED"); value != "" {
		enabled := envBool("INVOKR_SHELL_ENABLED", c.IsEnabled())
		c.Enabled = boolPtr(enabled)
	}
	return c
}

func (c MemoryConfig) withDefaults(homeDir string) MemoryConfig {
	c.Dir = cmp.Or(c.Dir, filepath.Join(homeDir))
	c.Zerfoo = c.Zerfoo.WithDefaults()
	c.RecallGate = c.RecallGate.withDefaults()
	c.Forgetting.HalfLifeDays = cmp.Or(c.Forgetting.HalfLifeDays, DefaultForgettingHalfLifeDays)
	c.Forgetting.MinRetention = cmp.Or(c.Forgetting.MinRetention, DefaultForgettingMinRetention)
	c.Forgetting.ReinforcementFactor = cmp.Or(c.Forgetting.ReinforcementFactor, DefaultForgettingReinforcementFactor)
	c.Forgetting.MaxStrength = cmp.Or(c.Forgetting.MaxStrength, DefaultForgettingMaxStrength)
	c.Forgetting.RawEventRetentionDays = cmp.Or(c.Forgetting.RawEventRetentionDays, DefaultForgettingRawEventRetentionDays)
	c.Forgetting.ExemptKinds = orSlice(c.Forgetting.ExemptKinds, append([]string(nil), DefaultForgettingExemptKinds...))
	return c
}

func (c RecallGateConfig) withDefaults() RecallGateConfig {
	c.NothinkSuffix = cmp.Or(c.NothinkSuffix, constants.DefaultRecallGateNothinkSuffix)
	c.MaxQueries = cmp.Or(c.MaxQueries, constants.DefaultRecallGateMaxQueries)
	c.PerQueryLimit = cmp.Or(c.PerQueryLimit, constants.DefaultRecallGatePerQueryLimit)
	c.MaxHits = cmp.Or(c.MaxHits, constants.DefaultRecallGateMaxHits)
	c.BudgetChars = cmp.Or(c.BudgetChars, constants.DefaultRecallGateBudgetChars)
	c.MinMessageChars = cmp.Or(c.MinMessageChars, constants.DefaultRecallGateMinMessageChars)
	c.TimeoutMS = cmp.Or(c.TimeoutMS, constants.DefaultRecallGateTimeoutMS)
	return c
}

func (c ZerfooMemoryConfig) WithDefaults() ZerfooMemoryConfig {
	c.EmbeddingModel = cmp.Or(c.EmbeddingModel, DefaultZerfooEmbeddingModel)
	c.SummarizationModel = cmp.Or(c.SummarizationModel, DefaultZerfooSummarizationModel)
	c.EmbeddingDimensions = cmp.Or(c.EmbeddingDimensions, DefaultZerfooEmbeddingDims)
	c.RequestTimeoutMS = cmp.Or(c.RequestTimeoutMS, 120000)
	c.MaxRawEventsBeforeSummary = cmp.Or(c.MaxRawEventsBeforeSummary, 12)
	c.MinImportanceToStore = cmp.Or(c.MinImportanceToStore, 0.35)
	c.MaxSummaryTokens = cmp.Or(c.MaxSummaryTokens, 512)
	return c
}

func (c WorkspaceConfig) withDefaults(homeDir string) WorkspaceConfig {
	if strings.TrimSpace(c.Root) == "" {
		if wd, err := os.Getwd(); err == nil {
			c.Root = wd
		}
	}
	if c.Root != "" {
		c.Root = filepath.Clean(c.Root)
	}
	for _, roots := range [][]string{c.ExtraRoots, c.ReadonlyRoots} {
		for i, root := range roots {
			roots[i] = filepath.Clean(root)
		}
	}
	// The spill dir joins the roots so read_file can page spilled tool
	// results; NewWorkspacePolicy deduplicates repeats.
	c.ExtraRoots = append(c.ExtraRoots, filepath.Join(homeDir, constants.SpillDirName))
	return c
}

func (c ProviderConfig) withDefaults() ProviderConfig {
	c.BaseURL = cmp.Or(c.BaseURL, constants.DefaultProviderBaseURL)
	c.HTTPTimeout = cmp.Or(c.HTTPTimeout, constants.DefaultHTTPTimeout)
	c.MaxRetries = cmp.Or(c.MaxRetries, constants.DefaultProviderRetries)
	return c
}

func (c AgentConfig) withDefaults() AgentConfig {
	// One decision, not two: whenever native tool definitions are sent, the
	// prompt uses the native contract (#446).
	c.NativeToolCalling = !c.NativeToolRequestsDisabled
	c.SystemPrompt = cmp.Or(c.SystemPrompt, constants.DefaultAgentSystemPrompt)
	// Provenance is recorded once and then left alone: the loader runs
	// withDefaults before PERSONA.md and the environment, and the bootstrap
	// runs WithDefaults again afterwards, so recomputing would erase whichever
	// of those supplied the prompt. The first-run config file persists the
	// default verbatim, hence the text comparison rather than an empty check.
	if c.PromptSource == "" {
		c.PromptSource = "config"
		if c.SystemPrompt == constants.DefaultAgentSystemPrompt {
			c.PromptSource = "default"
		}
	}
	c.Temperature = cmp.Or(c.Temperature, constants.DefaultAgentTemperature)
	c.NPredict = cmp.Or(c.NPredict, constants.DefaultAgentNPredict)
	c.MaxTurns = cmp.Or(c.MaxTurns, constants.DefaultAgentMaxTurns)
	c.ReadTurnWeight = cmp.Or(c.ReadTurnWeight, constants.DefaultReadTurnWeight)
	c.HardTurnMultiplier = cmp.Or(c.HardTurnMultiplier, constants.DefaultHardTurnMultiplier)
	c.TurnExtension = cmp.Or(c.TurnExtension, constants.DefaultTurnExtension)
	c.ToolQuotas = withDefaultQuotas(c.ToolQuotas)
	c.MaxRequestTokens = cmp.Or(c.MaxRequestTokens, constants.DefaultAgentMaxRequestTokens)
	c.ContextWarningPercent = cmp.Or(c.ContextWarningPercent, 95)
	c.ContextCompactPercent = cmp.Or(c.ContextCompactPercent, 99)
	c.ToolResultMaxTokens = cmp.Or(c.ToolResultMaxTokens, constants.ToolResultGateTokens)
	c.ToolResultStrategy = cmp.Or(c.ToolResultStrategy, constants.ToolResultStrategySpill)
	// read pages itself (offset/limit continuation markers); spilling its
	// pages would be circular, so it defaults to the legacy cap.
	if _, ok := c.ToolResultStrategies["read"]; !ok {
		if c.ToolResultStrategies == nil {
			c.ToolResultStrategies = map[string]string{}
		}
		c.ToolResultStrategies["read"] = constants.ToolResultStrategyCap
	}
	c.ParallelToolCalls = !c.ParallelToolCallsDisabled
	return c
}

// withDefaultQuotas fills in the default per-run tool quotas the config did not
// name, so one override does not silently drop the others.
func withDefaultQuotas(quotas map[string]int) map[string]int {
	out := constants.DefaultToolQuotas()
	maps.Copy(out, quotas)
	return out
}

func (c ToolboxConfig) WithDefaults(homeDir string) ToolboxConfig {
	c.Dir = cmp.Or(c.Dir, filepath.Join(homeDir, constants.DefaultToolboxDir))
	// A bare relative name ("toolbox") resolves against the Invokr home, not the
	// process working directory: agent-authored tools belong in ~/.invokr/toolbox,
	// not in whatever project the agent happened to be started in. An explicitly
	// relative path ("./toolbox", "../shared") stays CWD-relative so the
	// documented dev workflow (INVOKR_TOOLBOX_DIR=./toolbox) still works.
	if !filepath.IsAbs(c.Dir) && !strings.HasPrefix(c.Dir, ".") {
		c.Dir = filepath.Join(homeDir, c.Dir)
	}
	c.GoBin = cmp.Or(c.GoBin, constants.DefaultGoBin)
	c.NodeBin = cmp.Or(c.NodeBin, constants.DefaultNodeBin)
	c.PythonBin = cmp.Or(c.PythonBin, constants.DefaultPythonBin)
	c.TokfBin = cmp.Or(c.TokfBin, constants.DefaultTokfBin)
	c.GoCacheDir = cmp.Or(c.GoCacheDir, constants.DefaultGoCacheDir)
	c.PythonPipArgs = orSlice(c.PythonPipArgs, constants.DefaultPythonPipArgs())
	c.MaxOutputBytes = orPositive(c.MaxOutputBytes, constants.ToolboxDefaultMaxOutputBytes)
	c.MemoryLimitBytes = orPositive(c.MemoryLimitBytes, constants.ToolboxDefaultMemoryLimitBytes)
	c.ExecTimeoutMS = orPositive(c.ExecTimeoutMS, constants.ToolboxDefaultExecTimeoutMS)
	// Ceilings a plugin manifest's `limits` block is checked against.
	c.MaxLimitTimeoutMS = cmp.Or(c.MaxLimitTimeoutMS, constants.ToolboxMaxLimitTimeoutMS)
	c.MaxLimitMemoryBytes = cmp.Or(c.MaxLimitMemoryBytes, constants.ToolboxMaxLimitMemoryBytes)
	c.MaxLimitOutputBytes = cmp.Or(c.MaxLimitOutputBytes, constants.ToolboxMaxLimitOutputBytes)
	c.PackageIndexURL = cmp.Or(c.PackageIndexURL, constants.ToolboxDefaultPackageIndexURL)
	c.PackageMetadataURL = cmp.Or(c.PackageMetadataURL, constants.ToolboxDefaultPackageMetadataURL)
	c.PackageIndexHosts = orSlice(c.PackageIndexHosts, constants.DefaultToolboxIndexHosts())
	c.PackageArtifactHosts = orSlice(c.PackageArtifactHosts, constants.DefaultToolboxArtifactHosts())
	c.ReleaseCooldownDays = orPositive(c.ReleaseCooldownDays, constants.ToolboxDefaultReleaseCooldownDays)
	return c
}

func (c ShellConfig) withDefaults() ShellConfig {
	if c.Enabled == nil {
		c.Enabled = boolPtr(true)
	}
	c.AllowedCommands = constants.ShellAllowedCommands(c.AllowedCommands)
	return c
}

func (c WebSearchConfig) withDefaults() WebSearchConfig {
	c.Providers = orSlice(c.Providers, constants.DefaultWebSearchProviders())
	c.CustomHTTP.Method = cmp.Or(c.CustomHTTP.Method, constants.CustomHTTPSearchDefaultMethod)
	c.CustomHTTP.QueryParam = cmp.Or(c.CustomHTTP.QueryParam, constants.CustomHTTPSearchDefaultQueryParam)
	if len(c.CustomHTTP.Mappings) == 0 {
		c.CustomHTTP.Mappings = constants.DefaultCustomHTTPSearchMappings()
	}
	c.CacheDir = cmp.Or(c.CacheDir, constants.WebSearchDefaultCacheDir)
	c.SearXNG.Image = cmp.Or(c.SearXNG.Image, constants.SearXNGDefaultImage)
	c.SearXNG.Port = cmp.Or(c.SearXNG.Port, constants.SearXNGDefaultPort)
	c.SearXNG.Engine = cmp.Or(c.SearXNG.Engine, constants.SearXNGDefaultEngine)
	return c
}

func (c CodeInterpreterConfig) withDefaults(homeDir string) CodeInterpreterConfig {
	c.Backend = cmp.Or(c.Backend, constants.CodeInterpreterBackendProcess)
	c.PythonBin = cmp.Or(c.PythonBin, constants.DefaultPythonBin)
	c.SessionDir = cmp.Or(c.SessionDir, filepath.Join(homeDir, "code_interpreter"))
	c.DefaultTimeoutMS = cmp.Or(c.DefaultTimeoutMS, constants.CodeInterpreterDefaultTimeoutMS)
	c.MaxTimeoutMS = cmp.Or(c.MaxTimeoutMS, constants.CodeInterpreterMaxTimeoutMS)
	c.MaxOutputBytes = cmp.Or(c.MaxOutputBytes, constants.CodeInterpreterMaxOutputBytes)
	c.MaxArtifactBytes = cmp.Or(c.MaxArtifactBytes, constants.CodeInterpreterMaxArtifactBytes)
	c.MemoryLimitBytes = cmp.Or(c.MemoryLimitBytes, constants.CodeInterpreterMemoryLimitBytes)
	c.ContainerImage = cmp.Or(c.ContainerImage, constants.CodeInterpreterDefaultContainerImage)
	return c
}

func (c ShellConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// IsolateHostEnv reports whether shell commands should run without inheriting
// the full host process environment. Defaults to true (isolated) when unset.
func (c ShellConfig) IsolateHostEnv() bool { return c.IsolateEnv == nil || *c.IsolateEnv }

// IsolateHostEnv reports whether toolbox tools should run without inheriting
// the full host process environment. Defaults to true (isolated) when unset.
func (c ToolboxConfig) IsolateHostEnv() bool { return c.IsolateEnv == nil || *c.IsolateEnv }

func boolPtr(value bool) *bool { return &value }

func orPositive[T cmp.Ordered](value, fallback T) T {
	if value <= *new(T) {
		return fallback
	}
	return value
}

func orSlice[T any](values, fallback []T) []T {
	if len(values) == 0 {
		return fallback
	}
	return values
}

// InvokrHomeDir returns the resolved Invokr home directory (e.g. ~/.invokr),
// honoring the INVOKR_HOME override. Exported for callers outside the config
// package that need to fill in defaults relative to the same home, such as the
// toolbox loader.
func InvokrHomeDir() string {
	if dir := Env("INVOKR_HOME"); dir != "" {
		return dir
	}
	return defaultInvokrHomeDir()
}

func defaultInvokrHomeDir() string {
	if dir, err := UserInvokrHomeDir(); err == nil {
		return dir
	}
	return filepath.Join(os.TempDir(), "invokr")
}

// UserInvokrHomeDir is ~/.invokr, or an error when there is no home directory.
func UserInvokrHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) == "" {
		err = errors.New("home directory is empty")
	}
	if err != nil {
		return "", err
	}
	return filepath.Join(home, constants.InvokrHomeDirName), nil
}

// Env reads an environment variable with surrounding whitespace trimmed.
func Env(key string) string { return strings.TrimSpace(os.Getenv(key)) }

func envParse[T any](key string, fallback T, parse func(string) (T, error)) T {
	if value := Env(key); value != "" {
		if parsed, err := parse(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool { return envParse(key, fallback, strconv.ParseBool) }

func envFields(key string, fallback []string) []string {
	value := Env(key)
	if value == "" {
		return fallback
	}
	return strings.Fields(value)
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
	}
	return slices.DeleteFunc(parts, func(p string) bool { return p == "" })
}

func parseEnvMap(value string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(value, ",") {
		name, rawValue, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		out[strings.TrimSpace(name)] = rawValue
	}
	return out
}

func mergeEnvMap(base, overlay map[string]string) map[string]string {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(overlay))
	maps.Copy(out, base)
	maps.Copy(out, overlay)
	return out
}
