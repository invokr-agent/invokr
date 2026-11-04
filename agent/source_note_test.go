package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSourceNoteNamesCallPaths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  string
		wants string // empty means the call gets no note
	}{
		{name: "path", args: `{"path":"surface/acpagent/share.go"}`, wants: "surface/acpagent/share.go"},
		{name: "paths", args: `{"paths":["a.go","b.go"]}`, wants: "a.go, b.go"},
		{name: "both", args: `{"paths":["a.go"],"path":"b.go"}`, wants: "a.go, b.go"},
		{name: "no paths", args: `{"offset":1,"limit":20}`, wants: ""},
		{name: "empty object", args: `{}`, wants: ""},
		{name: "absent args", args: ``, wants: ""},
		{name: "wrong type", args: `{"path":7}`, wants: ""},
		{name: "path list", args: `{"paths":"a.go"}`, wants: ""},
	} {
		note := sourceNote(json.RawMessage(tc.args))
		if tc.wants == "" {
			if note != "" {
				t.Fatalf("%s: note %q, want none", tc.name, note)
			}
			continue
		}
		if !strings.Contains(note, tc.wants) {
			t.Fatalf("%s: note %q, want it to name %s", tc.name, note, tc.wants)
		}
		if !strings.Contains(note, "read-only copy") {
			t.Fatalf("%s: note %q, want it to say the copy is read-only", tc.name, note)
		}
	}
}
