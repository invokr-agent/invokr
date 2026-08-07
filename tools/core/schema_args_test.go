package core

import "testing"

func TestValidateArgsEmptySchemaAcceptsAnything(t *testing.T) {
	if err := ValidateArgs(nil, []byte(`{"anything":1}`)); err != nil {
		t.Fatalf("empty schema rejected args: %v", err)
	}
}

func TestValidateArgsRequiredAndTypes(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":  map[string]any{"type": "string"},
			"count": map[string]any{"type": "integer"},
			"mode":  map[string]any{"type": "string", "enum": []any{"fast", "slow"}},
		},
		"required":             []any{"path"},
		"additionalProperties": false,
	}
	cases := []struct {
		name    string
		args    string
		wantErr bool
	}{
		{"valid", `{"path":"a.txt","count":2,"mode":"fast"}`, false},
		{"missing required", `{"count":2}`, true},
		{"wrong type", `{"path":42}`, true},
		{"non-integer number", `{"path":"a","count":1.5}`, true},
		{"unknown property", `{"path":"a","nope":1}`, true},
		{"bad enum", `{"path":"a","mode":"medium"}`, true},
		{"not an object", `[1,2]`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateArgs(schema, []byte(tc.args))
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateArgs(%s) error = %v, wantErr = %v", tc.args, err, tc.wantErr)
			}
		})
	}
}

func TestValidateArgsEmptyArgumentsStillChecksRequired(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []string{"path"}}
	if err := ValidateArgs(schema, nil); err == nil {
		t.Fatal("empty arguments passed a schema with a required field")
	}
	if err := ValidateArgs(map[string]any{"type": "object"}, nil); err != nil {
		t.Fatalf("empty arguments rejected by a schema with no required fields: %v", err)
	}
}
