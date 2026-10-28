package system

import (
	"slices"

	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/system/apply_patch"
	"github.com/invokr-agent/invokr/tools/system/code_interpreter"
	"github.com/invokr-agent/invokr/tools/system/create_tool"
	"github.com/invokr-agent/invokr/tools/system/delegate"
	"github.com/invokr-agent/invokr/tools/system/edit_file"
	"github.com/invokr-agent/invokr/tools/system/file_search"
	"github.com/invokr-agent/invokr/tools/system/git"
	"github.com/invokr-agent/invokr/tools/system/lsp"
	"github.com/invokr-agent/invokr/tools/system/mcp"
	"github.com/invokr-agent/invokr/tools/system/memory"
	"github.com/invokr-agent/invokr/tools/system/read_file"
	"github.com/invokr-agent/invokr/tools/system/retrieval"
	"github.com/invokr-agent/invokr/tools/system/schedule"
	"github.com/invokr-agent/invokr/tools/system/session_search"
	"github.com/invokr-agent/invokr/tools/system/shell"
	"github.com/invokr-agent/invokr/tools/system/skill"
	"github.com/invokr-agent/invokr/tools/system/tmux"
	"github.com/invokr-agent/invokr/tools/system/todo"
	"github.com/invokr-agent/invokr/tools/system/toolbox"
	"github.com/invokr-agent/invokr/tools/system/web_fetch"
	"github.com/invokr-agent/invokr/tools/system/web_search"
	"github.com/invokr-agent/invokr/tools/system/workspace"

	"go.uber.org/zap"
)

// LoadSystemTools registers every native tool into caller and returns a
// release func that unsubscribes any event-bus handlers registered along the
// way. Call it when the caller's session ends so per-session registrations do
// not accumulate on the shared bus (#324).
func LoadSystemTools(
	deps core.CallerDeps,
	caller core.NativeRegistrar,
	l *zap.Logger,
) func() {
	wsp := workspace.NewWorkspacePolicy(deps.App.Workspace).Protect(deps.App.Approvals.GrantsFile)
	if deps.Policy != nil && deps.Policy.Mode() == "yolo" {
		wsp = wsp.Unrestricted()
	}

	// One advertised edit mechanism: edit. apply_patch stays registered
	// but deferred behind tool_search unless the allowlist names it — a model
	// that needs multi-file patches can discover it, without diluting tool
	// selection for everyone else.
	apply_patch.RegisterApplyPatchTool(caller, deps.Dispatcher, wsp,
		slices.Contains(deps.App.Tools.Allowlist, "apply_patch"))
	edit_file.RegisterEditFileTool(caller, deps.Dispatcher, wsp)
	file_search.RegisterFileSearchTool(caller, deps, wsp)
	git.RegisterGitTools(caller, deps, wsp)
	read_file.RegisterReadFileTool(caller, wsp)
	lsp.RegisterLSPTools(caller, deps, wsp)
	web_search.RegisterWebSearchTool(caller, deps)
	web_fetch.RegisterWebFetchTool(caller, deps)
	skill.RegisterSkillTools(caller, deps)
	memory.RegisterMemoryTools(caller, deps)
	delegate.RegisterDelegateTool(caller, deps)
	session_search.RegisterSessionSearchTool(caller, deps)
	retrieval.RegisterRetrievalTool(caller, deps, wsp)
	mcp.RegisterMCPTools(caller, deps, l)
	if deps.App.Shell.IsEnabled() {
		shell.RegisterShellTool(caller, deps, wsp)
	}
	code_interpreter.RegisterCodeInterpreterTool(caller, deps, wsp)
	tmux.RegisterTmuxTool(caller, deps, wsp)
	unregisterToolbox := toolbox.RegisterToolboxTools(caller, deps, l)
	create_tool.RegisterCreateToolTool(caller, deps)
	schedule.RegisterScheduleTool(caller, deps)
	if !deps.App.Tools.TodoDisabled {
		todo.RegisterTodoTool(caller)
	}
	return unregisterToolbox
}
