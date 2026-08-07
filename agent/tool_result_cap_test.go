package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/invokr-ai/invokr/constants"
)

func TestCapToolResultUnderCapPassthrough(t *testing.T) {
	small := strings.Repeat("a", 1000)
	if got := capToolResult(small, constants.ToolResultMaxContextTokens); got != small {
		t.Fatalf("under-cap output modified: len %d -> %d", len(small), len(got))
	}
	exact := strings.Repeat("b", constants.ToolResultMaxContextTokens*4)
	if got := capToolResult(exact, constants.ToolResultMaxContextTokens); got != exact {
		t.Fatalf("exactly-at-cap output modified")
	}
	if got := capToolResult("", constants.ToolResultMaxContextTokens); got != "" {
		t.Fatalf("empty output modified: %q", got)
	}
}

func TestCapToolResultOverCapTruncatesWithMarker(t *testing.T) {
	limit := constants.ToolResultMaxContextTokens * 4
	big := strings.Repeat("x", limit+5000)
	got := capToolResult(big, constants.ToolResultMaxContextTokens)
	if !strings.HasSuffix(got, "narrow the request or fetch it in smaller parts]") {
		t.Fatalf("missing truncation marker, tail: %q", got[len(got)-80:])
	}
	if !strings.Contains(got, "[output truncated at ~16000 tokens") {
		t.Fatalf("marker does not name the cap, tail: %q", got[len(got)-120:])
	}
	if len(got) >= len(big) {
		t.Fatalf("truncated output not smaller: %d >= %d", len(got), len(big))
	}
	if !strings.HasPrefix(got, big[:limit]) {
		t.Fatalf("truncated output does not preserve the leading %d bytes", limit)
	}
}

func TestCapToolResultUTF8Boundary(t *testing.T) {
	limit := constants.ToolResultMaxContextTokens * 4
	// Multi-byte runes so a naive byte cut would split one mid-sequence.
	big := strings.Repeat("日", limit/3+2000)
	got := capToolResult(big, constants.ToolResultMaxContextTokens)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated output is not valid UTF-8")
	}
	if !strings.Contains(got, "[output truncated at ~") {
		t.Fatalf("missing marker on multi-byte truncation")
	}
}

func TestCapToolResultKeepsJSONEnvelope(t *testing.T) {
	limit := constants.ToolResultMaxContextTokens * 4
	raw, err := json.Marshal(map[string]any{
		"stdout":    strings.Repeat("x", limit+50_000),
		"stderr":    "",
		"exit_code": 3,
		"timed_out": false,
		"artifacts": []string{"out/plot.png"},
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	got := capToolResult(string(raw), constants.ToolResultMaxContextTokens)
	if len(got) > limit {
		t.Fatalf("capped envelope over the limit: %d > %d", len(got), limit)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(got), &fields); err != nil {
		t.Fatalf("capped envelope is not valid JSON: %v", err)
	}
	if fields["exit_code"] != float64(3) || fields["timed_out"] != false {
		t.Fatalf("envelope lost its status fields: %v", fields)
	}
	if arts, _ := fields["artifacts"].([]any); len(arts) != 1 || arts[0] != "out/plot.png" {
		t.Fatalf("envelope lost its artifacts: %v", fields["artifacts"])
	}
	stdout, _ := fields["stdout"].(string)
	if !strings.HasSuffix(stdout, "smaller parts]") {
		t.Fatalf("stdout not marked as truncated: tail %q", stdout[max(len(stdout)-40, 0):])
	}
	if !strings.HasPrefix(stdout, "xxxx") {
		t.Fatalf("stdout lost its leading bytes")
	}
}

func TestCapToolResultTrimsSecondFieldWhenNeeded(t *testing.T) {
	limit := constants.ToolResultMaxContextTokens * 4
	raw, err := json.Marshal(map[string]any{
		"stdout":    strings.Repeat("x", limit*2/3),
		"stderr":    strings.Repeat("e", limit*2/3),
		"exit_code": 1,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	got := capToolResult(string(raw), constants.ToolResultMaxContextTokens)
	if len(got) > limit {
		t.Fatalf("capped envelope over the limit: %d > %d", len(got), limit)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(got), &fields); err != nil {
		t.Fatalf("capped envelope is not valid JSON: %v", err)
	}
	if fields["exit_code"] != float64(1) {
		t.Fatalf("envelope lost exit_code: %v", fields)
	}
}

func TestCapToolResultNonObjectJSONFallsBackToTruncation(t *testing.T) {
	limit := constants.ToolResultMaxContextTokens * 4
	raw, err := json.Marshal([]string{strings.Repeat("x", limit+1000)})
	if err != nil {
		t.Fatalf("marshal array: %v", err)
	}
	got := capToolResult(string(raw), constants.ToolResultMaxContextTokens)
	if !strings.HasSuffix(got, "smaller parts]") {
		t.Fatalf("array result not truncated with a marker")
	}
}
