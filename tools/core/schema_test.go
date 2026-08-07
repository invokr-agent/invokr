package core

import (
	"reflect"
	"testing"
)

type nested struct {
	Label string `json:"label"`
	Note  string `json:"note,omitempty" desc:"Optional note."`
}

type schemaArgs struct {
	Query    string            `json:"query" desc:"The query."`
	Mode     string            `json:"mode,omitempty" enum:"auto,literal" desc:"Search mode."`
	Limit    int               `json:"limit,omitempty"`
	Size     int64             `json:"size,omitempty"`
	Score    float64           `json:"score,omitempty"`
	DryRun   bool              `json:"dry_run,omitempty"`
	Lines    *int              `json:"lines,omitempty"`
	Tags     []string          `json:"tags,omitempty" enum:"a,b"`
	Items    []nested          `json:"items"`
	Free     map[string]string `json:"free,omitempty"`
	Skipped  string            `json:"-"`
	unwanted string
}

func TestSchemaFor(t *testing.T) {
	want := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"query":   map[string]any{"type": "string", "description": "The query."},
			"mode":    map[string]any{"type": "string", "enum": []string{"auto", "literal"}, "description": "Search mode."},
			"limit":   map[string]any{"type": "integer"},
			"size":    map[string]any{"type": "integer"},
			"score":   map[string]any{"type": "number"},
			"dry_run": map[string]any{"type": "boolean"},
			// A pointer field is optional and schematized as its element type.
			"lines": map[string]any{"type": "integer"},
			"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"a", "b"}}},
			"items": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"label": map[string]any{"type": "string"},
						"note":  map[string]any{"type": "string", "description": "Optional note."},
					},
					"required": []string{"label"},
				},
			},
			"free": map[string]any{"type": "object"},
		},
		"required": []string{"query", "items"},
	}
	if got := SchemaFor[schemaArgs](); !reflect.DeepEqual(got, want) {
		t.Fatalf("SchemaFor mismatch:\ngot  %#v\nwant %#v", got, want)
	}
	_ = schemaArgs{}.unwanted
}

func TestSchemaForWithEnum(t *testing.T) {
	schema := SchemaFor[schemaArgs](WithEnum("query", []string{"ls", "git"}))
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema has no properties map")
	}
	query, ok := properties["query"].(map[string]any)
	if !ok {
		t.Fatal("schema has no query property")
	}
	if got := query["enum"]; !reflect.DeepEqual(got, []string{"ls", "git"}) {
		t.Fatalf("query enum = %v, want [ls git]", got)
	}
	// An unknown property name is a no-op rather than a panic.
	SchemaFor[schemaArgs](WithEnum("nope", []string{"x"}))
}

type boundedArgs struct {
	Limit  int            `json:"limit,omitempty" min:"1" max:"10"`
	Opaque nested         `json:"opaque" schema:"object"`
	Detail nested         `json:"detail"`
	Extra  map[string]any `json:"extra,omitempty"`
}

func TestSchemaForBoundsAndOpaqueObjects(t *testing.T) {
	schema := SchemaFor[boundedArgs](WithOnly("limit", "opaque"), WithRequired("opaque"))
	want := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
			"opaque": map[string]any{"type": "object"},
		},
		"required": []string{"opaque"},
	}
	if !reflect.DeepEqual(schema, want) {
		t.Fatalf("SchemaFor mismatch:\ngot  %#v\nwant %#v", schema, want)
	}
	if _, ok := SchemaFor[boundedArgs](WithRequired())["required"]; ok {
		t.Fatal("WithRequired() should clear required")
	}
}
