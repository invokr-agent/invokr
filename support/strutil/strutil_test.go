package strutil

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFirstNonEmpty(t *testing.T) {
	if got := FirstNonEmpty("", "  ", " value ", "other"); got != " value " {
		t.Fatalf("FirstNonEmpty = %q, want %q", got, " value ")
	}
	if got := FirstNonEmpty("", "  "); got != "" {
		t.Fatalf("FirstNonEmpty = %q, want empty", got)
	}
}

func TestFirstNonEmptyTrimmed(t *testing.T) {
	if got := FirstNonEmptyTrimmed("", " value "); got != "value" {
		t.Fatalf("FirstNonEmptyTrimmed = %q, want %q", got, "value")
	}
}

func TestTruncateUTF8(t *testing.T) {
	if got := TruncateUTF8("short", 10); got != "short" {
		t.Fatalf("no-op truncate = %q", got)
	}
	s := strings.Repeat("é", 10) // 2 bytes per rune
	got := TruncateUTF8(s, 5)
	if len(got) > 5 || !utf8.ValidString(got) {
		t.Fatalf("TruncateUTF8 = %q (len %d), want valid UTF-8 within 5 bytes", got, len(got))
	}
	if got != strings.Repeat("é", 2) {
		t.Fatalf("TruncateUTF8 = %q, want %q", got, strings.Repeat("é", 2))
	}
}

func TestHashHex(t *testing.T) {
	// SHA-256 of the empty string.
	const empty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := HashHex(""); got != empty {
		t.Fatalf("HashHex(\"\") = %q, want %q", got, empty)
	}
	if HashHexTrimmed("  x  ") != HashHex("x") {
		t.Fatalf("HashHexTrimmed should hash the trimmed input")
	}
}

func TestExtractJSONObject(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                               `{"a":1}`,
		"prose before {\"a\":1} and {trailing}": `{"a":1}`,
		"```json\n{\"a\":{\"b\":2}}\n```":       `{"a":{"b":2}}`,
		`{"text":"a } in a string"}`:            `{"text":"a } in a string"}`,
		`{"escaped":"quote \" then }"}`:         `{"escaped":"quote \" then }"}`,
		"no json here":                          "no json here",
		`unterminated {"a":1`:                   `unterminated {"a":1`,
	}
	for in, want := range cases {
		if got := ExtractJSONObject(in); got != want {
			t.Errorf("ExtractJSONObject(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestExtractJSONObjectUndecodableSpans pins the inputs where this balanced-brace
// scanner returns a different span than the json.Decoder-based copy it replaced in
// capability/syntax (which returned "" on failure). Every caller feeds the result
// straight to a JSON decoder, so the contract that matters is the parse outcome:
// on these inputs both spans fail to decode, which is why the two implementations
// are interchangeable at their call sites.
func TestExtractJSONObjectUndecodableSpans(t *testing.T) {
	inputs := []string{
		// Leading non-JSON brace span before the real object: the old copy
		// returned "" (decoder error), this one returns "{ not json }".
		`text { not json } {"type":"reply","message":"hi"}`,
		// Backslash-escaped JSON nested in a string, outer object unterminated:
		// the old copy returned "", this one returns the input unchanged.
		`{"payload":"{\"type\":\"reply\"}"`,
		// Plain unterminated object.
		`unterminated {"a":1`,
	}
	for _, in := range inputs {
		span := ExtractJSONObject(in)
		var out map[string]any
		if err := json.Unmarshal([]byte(span), &out); err == nil {
			t.Errorf("ExtractJSONObject(%q) = %q, which decoded as JSON; callers rely on it failing", in, span)
		}
		// The empty string the old implementation returned must fail too.
		if err := json.Unmarshal(nil, &out); err == nil {
			t.Fatal("empty span unexpectedly decoded as JSON")
		}
	}
}
