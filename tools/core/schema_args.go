package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
)

// ValidateArgs checks raw tool-call arguments against a manifest-supplied JSON
// Schema. It covers the subset a tool manifest realistically declares — object
// `type`, `required`, per-property `type`/`enum`, and `additionalProperties:
// false` — so a malformed call is refused in-core, before any plugin process is
// launched. An empty schema validates everything, which is what keeps
// schema-less tools on their existing behavior.
func ValidateArgs(schema map[string]any, raw []byte) error {
	typ, _ := schema["type"].(string)
	if len(schema) == 0 || (typ != "" && typ != "object") {
		return nil
	}
	args := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return fmt.Errorf("arguments must be a JSON object: %w", err)
		}
	}
	for _, name := range schemaStrings(schema["required"]) {
		if _, ok := args[name]; !ok {
			return fmt.Errorf("missing required argument %q", name)
		}
	}
	return validateArgValues(schemaProperties(schema), args, schema["additionalProperties"] == false)
}

// validateArgValues checks each supplied argument against its property schema.
// When the schema is closed (`additionalProperties: false`), an argument it
// never declared is itself the error.
func validateArgValues(properties, args map[string]any, closed bool) error {
	for name, value := range args {
		property, declared := properties[name].(map[string]any)
		if !declared && closed {
			return fmt.Errorf("unknown argument %q", name)
		}
		if err := validateArgValue(property, value); err != nil {
			return fmt.Errorf("argument %q: %w", name, err)
		}
	}
	return nil
}

func validateArgValue(property map[string]any, value any) error {
	if typ, _ := property["type"].(string); typ != "" && !jsonValueMatches(typ, value) {
		return fmt.Errorf("expected %s", typ)
	}
	enum := schemaStrings(property["enum"])
	if len(enum) == 0 || slices.Contains(enum, fmt.Sprint(value)) {
		return nil
	}
	return fmt.Errorf("must be one of %s", strings.Join(enum, ", "))
}

// jsonValueMatches reports whether a decoded JSON value carries the declared
// type. Every value json.Unmarshal produces lands in one of the cases below, so
// a keyword outside the JSON type set ("int", "float", a typo) matches nothing
// and rejects every value. That fails closed — the schema is plugin-authored,
// so the only party a bad keyword can lock out is the plugin's own author, and
// the validation message names the keyword it did not satisfy.
func jsonValueMatches(typ string, value any) bool {
	switch v := value.(type) {
	case string:
		return typ == "string"
	case bool:
		return typ == "boolean"
	case []any:
		return typ == "array"
	case map[string]any:
		return typ == "object"
	case float64:
		return typ == "number" || (typ == "integer" && v == math.Trunc(v))
	case nil:
		return typ == "null"
	}
	return true
}

// schemaStrings reads a schema keyword holding a list (required, enum) as
// strings, tolerating both the []string a Go-derived schema carries and the
// []any a JSON-decoded manifest schema carries.
func schemaStrings(value any) []string {
	switch list := value.(type) {
	case []string:
		return list
	case []any:
		return MapSlice(list, func(item any) string { return fmt.Sprint(item) })
	}
	return nil
}
