// Package tool_search owns manager discovery and its native schema.
package tool_search

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/tools/core"
)

// RunShape is the manager call that runs an enabled optional tool.
const RunShape = `tools {"action":"run","name":"<tool>","arguments":{...}}`

type Args struct {
	Action     string          `json:"action" enum:"list,search,enable,disable,create,run" desc:"List/search never enable. Enable before run; create persists and enables a toolbox tool."`
	Query      string          `json:"query,omitempty" desc:"Search names, namespaces and descriptions."`
	Names      []string        `json:"names,omitempty" desc:"Optional tools to enable or disable."`
	Name       string          `json:"name,omitempty" desc:"Enabled target for run."`
	Arguments  json.RawMessage `json:"arguments,omitempty" schema:"object" desc:"Arguments for the run target."`
	Definition json.RawMessage `json:"definition,omitempty" schema:"object" desc:"Persistent toolbox definition for create."`
}

func RegisterManager(c core.NativeRegistrar, approver approval.Approver, execute func(context.Context, Args) (string, error), definition map[string]any) {
	schema := core.SchemaFor[Args]()
	schema["properties"].(map[string]any)["arguments"] = map[string]any{"type": "object", "description": "Arguments for the enabled run target."}
	schema["properties"].(map[string]any)["definition"] = definition

	core.Register(c, core.Tool[Args]{
		Spec:     core.ToolSpec{Name: "tools", Description: "List or search permitted tools. Enable an optional tool when it fits the task better than a shell workaround, then run it with {\"action\":\"run\",\"name\":...,\"arguments\":{...}}; disable unused tools, or create a persistent toolbox tool.", Parameters: schema, Metadata: core.ReadWorkspaceMetadata("system", core.ToolCapabilities{})},
		Approve:  approveEnable,
		Approver: approver,
		Execute:  execute,
	})
}

// approveEnable asks in Manual before the model widens its own toolset; Auto
// approves it as confined work. Create enables through create_tool, which
// carries its own approval.
func approveEnable(_ context.Context, args Args) core.ApprovalDecision {
	if args.Action != "enable" {
		return core.Auto()
	}
	names := strings.Join(args.Names, ", ")
	subject := "enable " + strings.Join(args.Names, " ")
	return core.Ask(approval.Request{Tool: "tools", Tier: "tool_enable", Confined: true, Subject: subject, GrantPattern: subject, Summary: "Enable tools: " + names})
}

type Listing struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Enabled     bool           `json:"enabled"`
	Protected   bool           `json:"protected"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

func List(specs []core.ToolSpec, enabled []string, query string, schemas []string, protected func(string) bool) []Listing {
	out := []Listing{}
	for _, spec := range specs {
		if !matchesQuery(spec, query) {
			continue
		}
		item := Listing{Name: spec.Name, Description: ShortDescription(spec.Description), Protected: protected(spec.Name)}
		item.Enabled = item.Protected || slices.Contains(enabled, spec.Name)
		if slices.Contains(schemas, spec.Name) {
			item.Parameters = spec.Parameters
		}
		out = append(out, item)
	}
	return out
}
func matchesQuery(spec core.ToolSpec, query string) bool {
	haystack := strings.ToLower(spec.Name + " " + spec.Metadata.Namespace + " " + spec.Description)
	for term := range strings.FieldsSeq(strings.ToLower(query)) {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}
func ShortDescription(description string) string {
	line, _, _ := strings.Cut(description, "\n")
	sentence, _, _ := strings.Cut(line, ". ")
	return strings.TrimSpace(sentence)
}

// Resolve decodes manager execution without invoking a handler. This keeps the
// actual target inside normal approval, quota, and outcome processing.
func Resolve(call *core.ToolCall, allowed, protected func(string) bool) (*core.ToolCall, error) {
	if call.Name != "tools" {
		return call, nil
	}
	var args Args
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, err
	}
	switch args.Action {
	case "create":
		if err := allowedCreation(args.Definition, allowed); err != nil {
			return nil, err
		}
		return managedTarget("create_tool", args.Definition, allowed, protected)
	case "run":
		if args.Name == "create_tool" {
			return nil, fmt.Errorf(`use tools.create: tools {"action":"create","definition":{...}}; put your complete create_tool arguments inside definition, not arguments`)
		}
		return managedTarget(args.Name, args.Arguments, allowed, protected)
	default:
		return call, nil
	}
}
func managedTarget(name string, args json.RawMessage, allowed, protected func(string) bool) (*core.ToolCall, error) {
	if protected(name) || name == "tool_search" {
		return nil, fmt.Errorf("call core tools directly; tools.run cannot invoke %q. Correct call: {\"name\":%q,\"arguments\":{...}}. Copy the inner arguments object unchanged; omit the tools/action/run wrapper", name, name)
	}
	if name == "" {
		return nil, fmt.Errorf("name is required: %s", RunShape)
	}
	if !allowed(name) {
		return nil, fmt.Errorf("tool %q is not permitted", name)
	}
	if len(args) == 0 || args[0] != '{' || !json.Valid(args) {
		return nil, fmt.Errorf("arguments must be a JSON object: %s", RunShape)
	}
	return &core.ToolCall{Name: name, Arguments: args, Managed: true}, nil
}

func allowedCreation(raw json.RawMessage, allowed func(string) bool) error {
	var definition struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &definition); err != nil {
		return err
	}
	if !allowed(definition.Name) {
		return fmt.Errorf("created tool %q is outside the operator allowlist", definition.Name)
	}
	return nil
}
