package core

import (
	"context"
	"encoding/json"
	"strings"
)

type NativeHandler func(context.Context, *ToolCall) (string, error)

type DynamicNativeTool struct {
	Spec    ToolSpec
	Handler NativeHandler
}

type NativeRegistrar interface {
	RegisterNative(ToolSpec, NativeHandler)
	HasNative(name string) bool
	// NativeSource returns the Metadata.Source of the registered tool with
	// this name, or "" if no tool is registered under it. Lets callers (e.g.
	// create_tool) distinguish a collision with a true native tool from one
	// with a toolbox-loaded tool, which overwrite:true is meant to replace.
	NativeSource(name string) string
	ReplaceNativeToolsBySource(source string, tools []DynamicNativeTool) []string
}

type ToolCall struct {
	// Managed is trusted execution metadata, never accepted from model JSON.
	Managed bool `json:"-"`
	// Note is trusted text appended to the model-facing result, e.g. after a
	// shell line was rerouted to a native tool.
	Note      string          `json:"-"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolSpec struct {
	Name, Description, Example string
	Parameters                 map[string]any
	Metadata                   ToolMetadata
}

type ToolMetadata struct {
	// ManagedApproval is set by trusted registration, not plugin metadata.
	ManagedApproval                       bool
	Namespace, Source, Runtime, PluginDir string
	Policy                                ToolPolicy
	Capabilities                          ToolCapabilities
	AlwaysVisible                         bool
	// Command optionally exposes the tool as a user-facing slash command.
	// Invocation goes through the normal tool-call path, so approval tiers
	// and standing grants apply exactly as if the model had called it.
	Command *ToolCommand
}

// ToolCommand declares a user-facing slash command for a tool. Name is the
// command as typed (without the slash); Usage and Summary feed /help.
type ToolCommand struct {
	Name    string `json:"name"`
	Usage   string `json:"usage,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type ToolPolicy struct {
	Tiers    []string `json:"tiers"`
	Approval string   `json:"approval"`
}

type ToolCapabilities struct {
	ReadsWorkspace  bool `json:"reads_workspace"`
	WritesWorkspace bool `json:"writes_workspace"`
	Network         bool `json:"network"`
	ExecutesCode    bool `json:"executes_code"`
}

func (spec ToolSpec) NativeToolDescription() string {
	description := strings.TrimSpace(spec.Description)
	example := strings.TrimSpace(spec.Example)
	if example == "" {
		return description
	}
	if description == "" {
		return "Example: " + example
	}
	return description + "\nExample: " + example
}

// CapabilityIndex maps tool name to declared capabilities, the shared query
// used by session policy and compaction's writes-workspace pinning.
func CapabilityIndex(specs []ToolSpec) map[string]ToolCapabilities {
	out := make(map[string]ToolCapabilities, len(specs))
	for _, spec := range specs {
		out[spec.Name] = spec.Metadata.Capabilities
	}
	return out
}
