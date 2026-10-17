package core

import (
	"cmp"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// SchemaOption adjusts the generated top-level schema after derivation.
type SchemaOption func(schema map[string]any)

// WithEnum sets an enum on one property whose allowed values are only known at
// runtime (e.g. the configured shell command allowlist).
func WithEnum(property string, values []string) SchemaOption {
	return func(schema map[string]any) {
		if prop, ok := schemaProperties(schema)[property].(map[string]any); ok {
			prop["enum"] = values
		}
	}
}

// WithOnly narrows the schema to the named properties, so several tools can
// share one argument struct and each advertise just the subset it uses.
func WithOnly(fields ...string) SchemaOption {
	return func(schema map[string]any) {
		maps.DeleteFunc(schemaProperties(schema), func(name string, _ any) bool {
			return !slices.Contains(fields, name)
		})
	}
}

// WithRequired overrides the derived required set, for the cases where a
// field's optionality does not follow from its json tag.
func WithRequired(fields ...string) SchemaOption {
	return func(schema map[string]any) {
		if len(fields) == 0 {
			delete(schema, "required")
			return
		}
		schema["required"] = fields
	}
}

func schemaProperties(schema map[string]any) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	return properties
}

// SchemaFor derives the model-facing JSON Schema for a tool's argument struct
// from its tags: `type` from the Go type, `required` from the absence of
// omitempty, and `description`/`enum` from the `desc` and `enum` tags. Deriving
// it keeps the schema and the struct the tool actually decodes from drifting
// apart — previously a new field could be added to the struct and silently
// omitted from the schema, leaving the model unaware the parameter existed.
func SchemaFor[A any](options ...SchemaOption) map[string]any {
	schema := objectSchema(reflect.TypeFor[A]())
	for _, option := range options {
		option(schema)
	}
	return schema
}

func objectSchema(typ reflect.Type) map[string]any {
	properties := map[string]any{}
	var required []string
	for i := range typ.NumField() {
		field := typ.Field(i)
		name, optional, ok := schemaFieldName(field)
		if !ok {
			continue
		}
		property := typeSchema(field.Type, field.Tag)
		if desc := field.Tag.Get("desc"); desc != "" {
			property["description"] = desc
		}
		properties[name] = property
		if !optional {
			required = append(required, name)
		}
	}
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// schemaFieldName resolves a field's wire name and whether the model may omit
// it. Unexported and json:"-" fields are not part of the schema.
func schemaFieldName(field reflect.StructField) (name string, optional, ok bool) {
	if !field.IsExported() {
		return "", false, false
	}
	name, rest, _ := strings.Cut(field.Tag.Get("json"), ",")
	if name == "-" {
		return "", false, false
	}
	name = cmp.Or(name, field.Name)
	optional = strings.Contains(rest, "omitempty") || field.Type.Kind() == reflect.Pointer
	return name, optional, true
}

// typeSchema maps a Go type to its JSON Schema fragment. An `enum` tag applies
// to the scalar itself, or to the element type of a slice.
func typeSchema(typ reflect.Type, tag reflect.StructTag) map[string]any {
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		// `schema:"object"` keeps a struct opaque to the model — advertised as a
		// bare object rather than field by field.
		if tag.Get("schema") == "object" {
			return map[string]any{"type": "object"}
		}
		return objectSchema(typ)
	case reflect.Slice:
		return map[string]any{"type": "array", "items": typeSchema(typ.Elem(), tag)}
	case reflect.Map, reflect.Interface:
		return map[string]any{"type": "object"}
	}
	schema := map[string]any{"type": jsonTypeName(typ.Kind())}
	if enum := tag.Get("enum"); enum != "" {
		schema["enum"] = strings.Split(enum, ",")
	}
	for tagName, key := range map[string]string{"min": "minimum", "max": "maximum"} {
		if bound, err := strconv.Atoi(tag.Get(tagName)); err == nil {
			schema[key] = bound
		}
	}
	return schema
}

// jsonTypeNames maps the Go kinds with a JSON Schema type of their own; every
// other kind (string included) is advertised as a string.
var jsonTypeNames = map[reflect.Kind]string{reflect.Bool: "boolean", reflect.Float32: "number", reflect.Float64: "number", reflect.Int: "integer", reflect.Int8: "integer", reflect.Int16: "integer", reflect.Int32: "integer", reflect.Int64: "integer", reflect.Uint: "integer", reflect.Uint8: "integer", reflect.Uint16: "integer", reflect.Uint32: "integer", reflect.Uint64: "integer"}

func jsonTypeName(kind reflect.Kind) string { return cmp.Or(jsonTypeNames[kind], "string") }
