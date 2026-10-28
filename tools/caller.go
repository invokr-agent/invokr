package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/capability/sessionstore"
	"github.com/invokr-agent/invokr/support/strutil"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/integrations"
	"github.com/invokr-agent/invokr/tools/system"
	"github.com/invokr-agent/invokr/tools/system/tool_search"

	"go.uber.org/zap"
)

type Caller struct {
	mu      sync.RWMutex
	specs   []core.ToolSpec
	native  map[string]nativeEntry
	managed map[string]sessionstore.ToolState
	app     core.CallerDeps
	logger  *zap.Logger
	filter  core.ResultFilter
	release func()
}

// Close releases session-scoped registrations made during tool loading —
// currently the toolbox refresh handler on the shared event bus. Callers that
// build a Caller per chat session must Close it when the session ends so
// handlers do not accumulate on the process-wide bus (#324). Idempotent.
func (c *Caller) Close() {
	c.mu.Lock()
	release := c.release
	c.release = nil
	c.mu.Unlock()
	if release != nil {
		release()
	}
}

type nativeEntry struct {
	spec    core.ToolSpec
	handler core.NativeHandler
}

func NewCaller(deps core.CallerDeps, logger ...*zap.Logger) *Caller {
	l := cmp.Or(append(slices.Clip(logger), zap.NewNop())...)
	deps.App = deps.App.WithDefaults()
	deps.App.Tools.Core = slices.Clone(deps.App.Tools.Core)
	filter := integrations.ResultFilterForConfig(deps.App.Toolbox, l)

	caller := &Caller{logger: l, specs: []core.ToolSpec{}, native: map[string]nativeEntry{}, app: deps, filter: filter}

	caller.release = system.LoadSystemTools(deps, caller, l)
	tool_search.RegisterManager(caller, deps.Approver, caller.manageTools, caller.native["create_tool"].spec.Parameters)
	return caller
}

func (c *Caller) HasNative(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.native[name]
	return ok
}

func (c *Caller) NativeSource(name string) string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.native[name].spec.Metadata.Source
}

func (c *Caller) Approver() approval.Approver {
	if c == nil {
		return nil
	}
	return c.app.Approver
}

func (c *Caller) Specs() []core.ToolSpec {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Clone(c.specs)
}

func (c *Caller) PromptSpecs(sessionID string) []core.ToolSpec {
	out := c.StableSpecs()
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, item := range c.managed[sessionID].Catalog {
		if c.advertised(item.Name) && !c.protectedTool(item.Name) {
			out = append(out, core.ToolSpec{Name: item.Name, Description: item.Description})
		}
	}
	return out
}

// StableSpecs keeps the configured allowlist while ignoring the mutable
// approval mode and per-session schema-loading state. HandleCall enforces policy.
func (c *Caller) StableSpecs() []core.ToolSpec {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []core.ToolSpec
	for _, name := range c.coreTools() {
		if entry, ok := c.native[name]; ok && c.advertised(name) {
			out = append(out, entry.spec)
		}
	}
	return out
}

// advertised reports whether name may be shown to, and called by, the model.
// An unset allowlist (the default) advertises every registered tool.
func (c *Caller) advertised(name string) bool {
	if name == "tools" {
		return true
	}
	allowlist := c.app.App.Tools.Allowlist
	return len(allowlist) == 0 || slices.Contains(allowlist, name)
}

func (c *Caller) HandleCall(ctx context.Context, call *core.ToolCall) (result string, err error) {
	if c == nil {
		return "", fmt.Errorf("caller is nil")
	}
	if call == nil {
		return "", fmt.Errorf("tool call is required")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			c.logger.Error("tool handler panicked", zap.Any("panic", recovered), zap.Stack("stack"))
			result, err = "", fmt.Errorf("tool panicked: %v", recovered)
		}
	}()
	if !c.advertised(call.Name) {
		return "", &core.ToolError{Code: "not_advertised", Payload: fmt.Sprintf("tool %q is not available in this session", call.Name)}
	}
	c.mu.RLock()
	entry := c.native[call.Name]
	c.mu.RUnlock()
	if entry.handler == nil {
		return "", fmt.Errorf("unknown tool %q", call.Name)
	}
	if err := c.checkManagedCall(ctx, call); err != nil {
		return "", err
	}
	callCtx, done, policyErr := c.beginCall(ctx, entry, call)
	defer done()
	if policyErr != nil {
		return "", policyErr
	}
	ctx = callCtx
	meta := core.ToolResultMeta{ToolName: call.Name, Runtime: entry.spec.Metadata.Runtime, Source: strutil.FirstNonEmptyTrimmed(entry.spec.Metadata.Source, "native")}
	result, err = entry.handler(ctx, call)
	if err != nil {
		return c.filterHandlerError(ctx, meta, err)
	}
	if call.Name == "create_tool" && call.Managed {
		result, err = c.createdTool(ctx, call, result)
		if err != nil {
			return "", err
		}
	}
	return c.filterResult(ctx, meta, result)
}

// filterHandlerError handles a tool handler error. A flagged-but-completed
// result (ToolError.Payload) still goes to the model, so it passes through the
// same result filter; anything else is a hard failure.
func (c *Caller) filterHandlerError(ctx context.Context, meta core.ToolResultMeta, err error) (string, error) {
	var te *core.ToolError
	if errors.As(err, &te) && te != nil && te.Payload != "" {
		filtered, ferr := c.filterResult(ctx, meta, te.Payload)
		if ferr != nil {
			return "", ferr
		}
		filtered = cmp.Or(filtered, "[output removed by filter]")
		te.Payload = filtered
		return filtered, err
	}
	return "", err
}

func (c *Caller) filterResult(ctx context.Context, meta core.ToolResultMeta, result string) (string, error) {
	if c == nil || c.filter == nil {
		return result, nil
	}
	return c.filter.FilterToolResult(ctx, meta, result)
}

func (c *Caller) RegisterNative(spec core.ToolSpec, handler core.NativeHandler) {
	if c == nil || handler == nil {
		return
	}
	spec.Metadata.Source = strutil.FirstNonEmptyTrimmed(spec.Metadata.Source, "native")
	spec.Metadata.Namespace = strutil.FirstNonEmptyTrimmed(spec.Metadata.Namespace, defaultNativeNamespace(spec.Name))
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.native == nil {
		c.native = map[string]nativeEntry{}
	}
	c.native[spec.Name] = nativeEntry{spec: spec, handler: handler}
	c.specs = core.UpsertToolSpec(c.specs, spec)
}

func (c *Caller) ForgetSession(id string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.managed, id)
}

func (c *Caller) ReplaceNativeToolsBySource(source string, tools []core.DynamicNativeTool) []string {
	if c == nil || strings.TrimSpace(source) == "" {
		return nil
	}
	source = strings.TrimSpace(source)
	var skipped []string
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeNativeToolsBySourceLocked(source)
	if c.native == nil {
		c.native = map[string]nativeEntry{}
	}
	for _, tool := range tools {
		if !validDynamicNativeTool(tool) {
			continue
		}
		spec := normalizedDynamicToolSpec(tool.Spec, source)
		if existing, ok := c.native[spec.Name]; ok && existing.spec.Metadata.Source != source {
			skipped = append(skipped, tool.Spec.Name)
			continue
		}
		c.native[spec.Name] = nativeEntry{spec: spec, handler: tool.Handler}
		c.specs = core.UpsertToolSpec(c.specs, spec)
	}
	return skipped
}

func (c *Caller) removeNativeToolsBySourceLocked(source string) {
	maps.DeleteFunc(c.native, func(_ string, entry nativeEntry) bool { return entry.spec.Metadata.Source == source })
	c.specs = slices.DeleteFunc(c.specs, func(spec core.ToolSpec) bool { return spec.Metadata.Source == source })
}

func validDynamicNativeTool(tool core.DynamicNativeTool) bool {
	return tool.Handler != nil && strings.TrimSpace(tool.Spec.Name) != ""
}

func normalizedDynamicToolSpec(spec core.ToolSpec, source string) core.ToolSpec {
	spec.Metadata.Source = strutil.FirstNonEmptyTrimmed(spec.Metadata.Source, source)
	spec.Metadata.Namespace = strutil.FirstNonEmptyTrimmed(spec.Metadata.Namespace, defaultNativeNamespace(spec.Name))
	return spec
}

// nativeNamespaces overrides the default "system" namespace for the tools that
// belong to another one.
var nativeNamespaces = map[string]string{"file_search": "workspace", "apply_patch": "workspace", "load_skill": "skill", "memory": "memory", "recall": "memory"}

func defaultNativeNamespace(name string) string { return cmp.Or(nativeNamespaces[name], "system") }

// AvailableSpecs is the live permitted catalog for UI and runtime discovery.
func (c *Caller) AvailableSpecs() []core.ToolSpec {
	return slices.DeleteFunc(c.Specs(), func(spec core.ToolSpec) bool { return !c.advertised(spec.Name) || managementHidden(spec.Name) })
}
