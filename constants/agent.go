package constants

import "slices"

const (
	DefaultAgentSystemPrompt = `You are Invokr, a coding agent in a real terminal workspace.

Complete requested changes: implement, run, and resolve blockers. Follow explicit scope; use judgment for unspecified work without overbuilding.
Choose the smallest working solution. Reuse existing code and the standard library before adding code or dependencies; review and trim the diff.
Use delegate_task for broad searches, long logs, speculative investigations, or repeated work across more than three files when only the conclusion is needed. Handle small, focused work directly.
If you need a file's contents, read it; if you need a command's output, run it. State uncertainty instead of guessing.
Be brief: lead with results, include failures and skipped work, and do not restate the request.`
	// DefaultAgentTemperature matches the sampling the local model servers are
	// launched with (the local ornith-9b llama-server runs --temperature 1.0),
	// so invokr no longer overrides it with a lower value the other harnesses
	// never send (#577 parity, #596).
	DefaultAgentTemperature = 1.0
	// DefaultAgentNPredict caps one completion's output tokens. A small model
	// asked to write a whole file inlines it into the tool call's arguments, so
	// a tight cap truncates the JSON mid-string and the call is rejected as
	// malformed. 8192 left headroom for a file-sized argument but not for a
	// reasoning model: a completion that thinks past the cap is discarded
	// whole (#596). 16384 gives reasoning room; Pi asks for 16384 as well.
	DefaultAgentNPredict, DefaultAgentMaxTurns = 16384, 40
	// DefaultAgentMaxRequestTokens bounds one request's token spend, counted
	// as each turn's completion plus its new context (config.MaxRequestTokens).
	// It is the backstop that keeps the turn ceiling safe on providers that
	// report no usage, where it is enforced from a local estimate.
	// Sized for long runs: 400k was ~15 min of a 79 tok/s local model and cut a
	// 1–2 h marathon task off at a third of its turns and wall clock (#496).
	DefaultAgentMaxRequestTokens = 1_500_000
	// MaxSummarizerTokens caps one compaction summary's completion. Left
	// uncapped, summaries came back at 6k-8.8k tokens - 13% of a 65536-token
	// window spent the moment the summary lands (#506). The effective cap is
	// min(MaxSummarizerTokens, window/SummarizerWindowFraction) so a small
	// window gets a proportionally smaller summary.
	MaxSummarizerTokens, SummarizerWindowFraction = 4096, 16
	// DefaultReadTurnWeight is what a turn that only read the workspace costs
	// against the turn budget: looking is cheap and rarely the reason a run
	// runs out of room, so half price. 1 restores the static count.
	DefaultReadTurnWeight = 0.5
	// DefaultHardTurnMultiplier bounds the raw loop iterations at
	// MaxTurns * this, whatever the weighted budget says, so a run whose turns
	// all cost zero cannot spin. 1 pins the loop to MaxTurns exactly.
	DefaultHardTurnMultiplier = 2.0
	// DefaultTurnExtension is how many turns a still-progressing run is granted
	// when its budget runs out, up to the hard ceiling. A negative value
	// disables the extension.
	DefaultTurnExtension = 10
	// EmptyModelResponseFormat carries why the turn came back blank: a spent
	// turn budget and a model that simply said nothing read identically
	// otherwise, and operators cannot tell which they hit.
	EmptyModelResponseFormat = "The model returned an empty response (%s). Please try rephrasing your request."
	// MinimalToolPreset is the name a tools allowlist can carry instead of a
	// tool list: INVOKR_TOOLS=minimal advertises only what a solo coding or
	// terminal task can use, so a small model is not choosing among 23 tools
	// of which 17 are noise.
	MinimalToolPreset = "minimal"
)

// DefaultToolQuotas caps how many times one run may call the tools whose value
// comes from outside the workspace and rarely improves on repetition: search
// and fetch grinds, and delegation fan-out. A quota of 0 or less is unlimited,
// and every tool not named here is unlimited.
func DefaultToolQuotas() map[string]int {
	return map[string]int{"web_search": 5, "web_fetch": 8, "delegate": 3}
}

// MinimalTools is the tool set MinimalToolPreset expands to.
var MinimalTools = []string{"shell", "read", "edit", "file_search", "git_read"}

// EagerSchemaTools ship their full schema on every turn. Every other tool is
// listed by name and description only until tool_search or a first call loads
// it: the full native set cost ~21k tokens of a 131k window per turn (#480).
// tmux is included despite the ~410-token cost of its schema (1634 chars):
// a deferred schema meant the model never discovered the tool at all in a
// marathon run (0 tool_search calls), even with the #527 shell hint (#541).
// delegate_task likewise: the prompt tells the model to delegate, but 25
// plain bench runs made 0 tool_search calls and 0 delegations while its
// schema was deferred.
var EagerSchemaTools = slices.Concat(MinimalTools, []string{"tool_search", "todo", "tmux", "delegate_task"})
