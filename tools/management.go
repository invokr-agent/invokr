package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/invokr-ai/invokr/capability/sessionstore"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/shell"
	"github.com/invokr-ai/invokr/tools/system/tool_search"
)

func (c *Caller) coreTools() []string {
	return core.DedupPreserveOrder(slices.Concat(constants.ManagedCoreTools, c.app.App.Tools.Core))
}
func (c *Caller) protectedTool(name string) bool {
	return slices.Contains(constants.ManagedCoreTools, name) || slices.Contains(c.app.App.Tools.Core, name)
}
func managementHidden(name string) bool { return name == "tool_search" || name == "create_tool" }

// StartToolSession restores state before the prompt or native schema snapshot.
func (c *Caller) StartToolSession(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.managed == nil {
		c.managed = map[string]sessionstore.ToolState{}
	}
	if _, ok := c.managed[id]; ok {
		return nil
	}
	state, err := c.toolStore().LoadToolState(id)
	if err != nil {
		return fmt.Errorf("load tool state: %w", err)
	}
	state.Enabled = slices.DeleteFunc(state.Enabled, func(name string) bool { return !c.optionalAllowed(name) })
	if state.Catalog == nil {
		state.Catalog = []sessionstore.ToolListing{}
		for _, spec := range c.specs {
			if c.optionalAllowed(spec.Name) {
				state.Catalog = append(state.Catalog, sessionstore.ToolListing{Name: spec.Name, Description: tool_search.ShortDescription(spec.Description)})
			}
		}
	}
	if err := c.toolStore().SaveToolState(id, state); err != nil {
		return err
	}
	c.managed[id] = state
	return nil
}
func (c *Caller) toolStore() *sessionstore.Store { return sessionstore.New(c.app.App.Memory.Dir) }
func (c *Caller) optionalAllowed(name string) bool {
	_, exists := c.native[name]
	return exists && c.advertised(name) && !c.protectedTool(name) && !managementHidden(name)
}

// ToolListings returns unfiltered manager state for local UI consumers.
func (c *Caller) ToolListings(ctx context.Context, id string) ([]tool_search.Listing, error) {
	if err := c.StartToolSession(id); err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.toolListings(ctx, id, "", nil), nil
}

func (c *Caller) SetToolEnabled(ctx context.Context, id, name string, enabled bool) error {
	action := "disable"
	if enabled {
		action = "enable"
	}
	_, err := c.manageTools(core.WithSessionID(ctx, id), tool_search.Args{Action: action, Names: []string{name}})
	return err
}

func (c *Caller) manageTools(ctx context.Context, args tool_search.Args) (string, error) {
	id := core.SessionIDFromContext(ctx)
	if id == "" {
		return "", fmt.Errorf("tools requires a session")
	}
	if err := c.StartToolSession(id); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch args.Action {
	case "list", "search":
		return core.MarshalResult("tools", c.toolListings(ctx, id, args.Query, nil))
	case "enable", "disable":
		return c.changeEnabled(ctx, id, args)
	default:
		return "", fmt.Errorf("unknown tools action %q: use list, search, enable, disable, create, or run; an enabled optional tool runs as %s", args.Action, tool_search.RunShape)
	}
}

func (c *Caller) toolListings(ctx context.Context, id, query string, schemas []string) []tool_search.Listing {
	specs := slices.DeleteFunc(slices.Clone(c.specs), func(spec core.ToolSpec) bool {
		return !c.advertised(spec.Name) || managementHidden(spec.Name) || !toolInScope(ctx, spec.Name)
	})
	return tool_search.List(specs, c.managed[id].Enabled, query, schemas, c.protectedTool)
}

func (c *Caller) changeEnabled(ctx context.Context, id string, args tool_search.Args) (string, error) {
	if len(args.Names) == 0 {
		return "", fmt.Errorf("names is required")
	}
	for _, name := range args.Names {
		if !c.optionalAllowed(name) || !toolInScope(ctx, name) {
			return "", fmt.Errorf("%q is protected, unavailable, or not permitted", name)
		}
	}
	state := c.managed[id]
	state.Enabled = slices.DeleteFunc(slices.Clone(state.Enabled), func(name string) bool { return slices.Contains(args.Names, name) })
	if args.Action == "enable" {
		state.Enabled = append(state.Enabled, args.Names...)
	}
	slices.Sort(state.Enabled)
	state.Enabled = slices.Compact(state.Enabled)
	if err := c.toolStore().SaveToolState(id, state); err != nil {
		return "", err
	}
	c.managed[id] = state
	return core.MarshalResult("tools", c.toolListings(ctx, id, "", args.Names))
}

// ResolveToolCall resolves before target policy, quotas and execution bookkeeping.
func (c *Caller) ResolveToolCall(ctx context.Context, call *core.ToolCall) (*core.ToolCall, error) {
	available := func(name string) bool { return c.advertised(name) && toolInScope(ctx, name) }
	// Inline Python belongs in the sandboxed interpreter, which the model
	// opts into; where there is none, the shell still runs it.
	if available(constants.CodeInterpreterToolName) && shell.InlinePython(call) {
		return nil, fmt.Errorf("inline Python runs in %[1]s, not the shell: enable it with {\"action\":\"enable\",\"names\":[\"%[1]s\"]}, then {\"action\":\"run\",\"name\":\"%[1]s\",\"arguments\":{\"code\":...}}; it reads the workspace, and output_files copy results back. A script or module (python3 x.py, python3 -m ...) still runs in the shell", constants.CodeInterpreterToolName)
	}
	call, err := tool_search.Resolve(call, available, c.protectedTool)
	if err != nil {
		return nil, err
	}
	// A shell line a native tool covers runs as that tool; only core tools,
	// so managed sessions accept the rewritten call without tools.run.
	return shell.Reroute(call, func(name string) bool { return available(name) && c.protectedTool(name) && c.registered(name) }), nil
}

func (c *Caller) registered(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.native[name].handler != nil
}

func (c *Caller) checkManagedCall(ctx context.Context, call *core.ToolCall) error {
	id := core.SessionIDFromContext(ctx)
	c.mu.RLock()
	defer c.mu.RUnlock()
	state, active := c.managed[id]
	if !active || c.protectedTool(call.Name) {
		return nil
	}
	if !call.Managed {
		return fmt.Errorf("optional tool %q must be called through tools.run after enabling", call.Name)
	}
	if call.Name != "create_tool" && !slices.Contains(state.Enabled, call.Name) {
		return fmt.Errorf("tool %q is disabled; use tools.enable first", call.Name)
	}
	return nil
}

func (c *Caller) createdTool(ctx context.Context, call *core.ToolCall, result string) (string, error) {
	var definition struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(call.Arguments, &definition); err != nil {
		return "", err
	}
	schemas, err := c.manageTools(ctx, tool_search.Args{Action: "enable", Names: []string{definition.Name}})
	if err != nil {
		return "", fmt.Errorf("tool created but not enabled: %w", err)
	}
	return core.MarshalResult("tools", map[string]any{"created": json.RawMessage(result), "availability": json.RawMessage(schemas)})
}

// ToolStateReminder appends compact authoritative state after compaction.
func (c *Caller) ToolStateReminder(id string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	state, ok := c.managed[id]
	if !ok {
		return ""
	}
	names, _ := json.Marshal(state.Enabled)
	return "Enabled optional tools: " + string(names) + ". All other optional tools are disabled. Use tools.enable to retrieve schemas again; tools.run executes one enabled target."
}

func toolInScope(ctx context.Context, name string) bool {
	names, scoped := ctx.Value(core.ToolScopeKey{}).(map[string]struct{})
	_, allowed := names[name]
	return !scoped || allowed || name == "tools"
}
