package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

func operation(spec core.ToolSpec, call *core.ToolCall) approval.Request {
	caps := spec.Metadata.Capabilities
	readOnly := spec.Metadata.Source == "native" && !caps.WritesWorkspace && !caps.ExecutesCode
	if spec.Metadata.Namespace == "lsp" {
		readOnly = false
	} // An LSP server executes workspace-selected code on the host.
	var args map[string]json.RawMessage
	_ = json.Unmarshal(call.Arguments, &args)
	subject := strings.TrimSpace(string(call.Arguments))
	// A read carries every file in paths, so a deny grant has to see them all;
	// matching only the first would let a batched call slip past it (#577).
	var paths []string
	if json.Unmarshal(args["paths"], &paths) == nil && len(paths) > 0 {
		subject = paths[0]
	}
	for _, key := range []string{"command", "path", "url"} {
		var value string
		if json.Unmarshal(args[key], &value) == nil && value != "" {
			subject = value
			break
		}
	}
	return approval.Request{Tool: spec.Name, Tier: spec.Metadata.Policy.Approval, Subject: subject, Paths: paths,
		Summary: "Run " + spec.Name, Details: string(call.Arguments), ReadOnly: readOnly,
		Writes: caps.WritesWorkspace, Network: caps.Network}
}

func planEligible(spec core.ToolSpec) bool {
	if operation(spec, &core.ToolCall{}).ReadOnly {
		return true
	}
	// These native owners validate prepared operations and build read-only execution plans.
	return spec.Metadata.Source == "native" && slices.Contains([]string{"shell", "git_read", "code_interpreter", "delegate_task", "tmux", "process", "edit", "apply_patch"}, spec.Name)
}

func (c *Caller) beginCall(ctx context.Context, entry nativeEntry, call *core.ToolCall) (context.Context, func(), error) {
	if c.app.Policy == nil {
		return ctx, func() {}, nil
	}
	req := operation(entry.spec, call)
	if req.ReadOnly && entry.spec.Metadata.Capabilities.ReadsWorkspace {
		wsp := workspace.NewWorkspacePolicy(c.app.App.Workspace).Unrestricted()
		// Resolve every path a batched read carries: replacing the list with
		// the subject alone left a deny grant blind to path[1:] (#577).
		if len(req.Paths) > 0 {
			req.Paths = resolveEach(wsp, req.Paths)
		} else if path, err := wsp.Resolve(req.Subject); err == nil {
			req.Paths = []string{wsp.DisplayPath(path)}
		}
	}
	ctx, done := c.app.Policy.Begin(ctx, req, c.app.Approver)
	if approval.ModeFromContext(ctx) == approval.Plan && !planEligible(entry.spec) {
		return ctx, done, &approval.DeniedError{Tool: call.Name, Reason: "tool has no read-only Plan execution path"}
	}
	if !entry.spec.Metadata.ManagedApproval {
		_, err := approval.Authorize(ctx, c.app.Approver, approval.CallRequest(ctx), false)
		return ctx, done, err
	}
	return ctx, done, nil
}

// resolveEach maps paths through the workspace policy, keeping any that do not
// resolve as written so a deny grant still matches on the raw argument.
func resolveEach(wsp workspace.Policy, paths []string) []string {
	return core.MapSlice(paths, func(p string) string {
		if abs, err := wsp.Resolve(p); err == nil {
			return wsp.DisplayPath(abs)
		}
		return p
	})
}
