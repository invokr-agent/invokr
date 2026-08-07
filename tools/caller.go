package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/integrations"
	"github.com/invokr-ai/invokr/tools/system"
	"maps"
	"slices"
	"strings"
	"sync"

	"go.uber.org/zap"
)

type Caller struct {
	mu            sync.RWMutex
	specs         []core.ToolSpec
	native        map[string]nativeEntry
	loadedSchemas map[string]map[string]struct{}
	app           core.CallerDeps
	logger        *zap.Logger
	filter        core.ResultFilter
	revision      uint64
	release       func()
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

// Revision changes whenever visible/callable tool definitions may change.
func (c *Caller) Revision() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.revision + c.app.Policy.Revision()
}

type nativeEntry struct {
	spec    core.ToolSpec
	handler core.NativeHandler
}

func NewCaller(deps core.CallerDeps, logger ...*zap.Logger) *Caller {
	l := callerLogger(logger...)
	deps.App = deps.App.WithDefaults()
	filter := integrations.ResultFilterForConfig(deps.App.Toolbox, l)

	caller := &Caller{logger: l, specs: []core.ToolSpec{}, native: map[string]nativeEntry{}, loadedSchemas: map[string]map[string]struct{}{}, app: deps, filter: filter}

	caller.release = system.LoadSystemTools(deps, caller, l)
	return caller
}

func callerLogger(logger ...*zap.Logger) *zap.Logger {
	if len(logger) > 0 && logger[0] != nil {
		return logger[0]
	}
	return zap.NewNop()
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
	entry, ok := c.native[name]
	if !ok {
		return ""
	}
	return entry.spec.Metadata.Source
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
	if len(c.specs) == 0 {
		return nil
	}
	return slices.Clone(c.specs)
}

func (c *Caller) PromptSpecs(sessionID string) []core.ToolSpec {
	return c.visibleSpecs(sessionID, false)
}

func (c *Caller) CallableSpecs(sessionID string) []core.ToolSpec {
	return c.visibleSpecs(sessionID, true)
}

// advertised reports whether name may be shown to, and called by, the model.
// An unset allowlist (the default) advertises every registered tool.
func (c *Caller) advertised(name string) bool {
	allowlist := c.app.App.Tools.Allowlist
	return len(allowlist) == 0 || slices.Contains(allowlist, name)
}

func (c *Caller) visibleSpecs(sessionID string, callableOnly bool) []core.ToolSpec {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]core.ToolSpec, 0, len(c.specs))
	for _, spec := range c.specs {
		if c.app.Policy != nil && c.app.Policy.Mode() == approval.Plan && !planEligible(spec) {
			continue
		}
		if !c.advertised(spec.Name) {
			continue
		}
		loaded := spec.Metadata.AlwaysVisible || c.schemaLoadedLocked(sessionID, spec.Name)
		if callableOnly && !loaded {
			continue
		}
		if !callableOnly && !loaded {
			spec.Example = ""
			spec.Parameters = nil
		}
		out = append(out, spec)
	}
	return out
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
	sessionID := core.SessionIDFromContext(ctx)
	// A first call to a deferred tool loads its schema for the next turn, so
	// a model that already knows the tool is not sent through tool_search;
	// if the guessed arguments are wrong the retry sees the schema (#480).
	if sessionID != "" && !c.toolCallable(sessionID, call.Name) {
		c.MarkSchemasLoaded(sessionID, []string{call.Name})
	}
	c.mu.RLock()
	entry := c.native[call.Name]
	c.mu.RUnlock()
	if entry.handler != nil {
		callCtx, done, policyErr := c.beginCall(ctx, entry, call)
		defer done()
		if policyErr != nil {
			return "", policyErr
		}
		ctx = callCtx
		meta := core.ToolResultMeta{ToolName: call.Name, Runtime: entry.spec.Metadata.Runtime, Source: core.FirstNonEmptyTrimmed(entry.spec.Metadata.Source, "native")}
		result, err := entry.handler(ctx, call)
		if err != nil {
			return c.filterHandlerError(ctx, meta, err)
		}
		return c.filterResult(ctx, meta, result)
	}

	return "", fmt.Errorf("unknown tool %q", call.Name)
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
		if filtered == "" {
			filtered = "[output removed by filter]"
		}
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
	spec.Metadata.Source = core.FirstNonEmptyTrimmed(spec.Metadata.Source, "native")
	spec.Metadata.Namespace = core.FirstNonEmptyTrimmed(spec.Metadata.Namespace, defaultNativeNamespace(spec.Name))
	// Only the core set ships its schema every turn; the rest is deferred
	// behind tool_search unless an allowlist names it (#480).
	if len(c.app.App.Tools.Allowlist) == 0 && !slices.Contains(constants.EagerSchemaTools, spec.Name) {
		spec.Metadata.AlwaysVisible = false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.native == nil {
		c.native = map[string]nativeEntry{}
	}
	c.native[spec.Name] = nativeEntry{spec: spec, handler: handler}
	c.specs = core.UpsertToolSpec(c.specs, spec)
	c.revision++
}

func (c *Caller) MarkSchemasLoaded(sessionID string, names []string) {
	if c == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loadedSchemas == nil {
		c.loadedSchemas = map[string]map[string]struct{}{}
	}
	if c.loadedSchemas[sessionID] == nil {
		c.loadedSchemas[sessionID] = map[string]struct{}{}
	}
	maps.Copy(c.loadedSchemas[sessionID], core.StringSet(names))
	c.revision++
}

// ForgetSession drops the schema-loaded state tracked for a session. Session
// owners must call it (via Session.Close) when the session ends so entries do
// not accumulate on a long-lived Caller shared across sessions (#317 B11).
// No-op for unknown or blank IDs; idempotent.
func (c *Caller) ForgetSession(sessionID string) {
	if c == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.loadedSchemas[sessionID]; !ok {
		return
	}
	delete(c.loadedSchemas, sessionID)
	c.revision++
}

func (c *Caller) toolCallable(sessionID, name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	i := slices.IndexFunc(c.specs, func(spec core.ToolSpec) bool { return spec.Name == name })
	// Fail-open is intentional: an unregistered spec means this gate can't
	// evaluate visibility at all, so it defers to the tool dispatcher itself
	// to reject unknown tool names rather than silently blocking calls here.
	if i < 0 {
		return true
	}
	return c.specs[i].Metadata.AlwaysVisible || c.schemaLoadedLocked(sessionID, name)
}

func (c *Caller) schemaLoadedLocked(sessionID, name string) bool {
	if strings.TrimSpace(sessionID) == "" {
		return true
	}
	_, ok := c.loadedSchemas[sessionID][name]
	return ok
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
	c.revision++
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
	spec.Metadata.Source = core.FirstNonEmptyTrimmed(spec.Metadata.Source, source)
	spec.Metadata.Namespace = core.FirstNonEmptyTrimmed(spec.Metadata.Namespace, defaultNativeNamespace(spec.Name))
	return spec
}

// nativeNamespaces overrides the default "system" namespace for the tools that
// belong to another one.
var nativeNamespaces = map[string]string{"file_search": "workspace", "apply_patch": "workspace", "load_skill": "skill", "memory": "memory", "recall": "memory"}

func defaultNativeNamespace(name string) string { return cmp.Or(nativeNamespaces[name], "system") }
