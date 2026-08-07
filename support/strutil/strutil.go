// Package strutil holds small string helpers shared across capability, tools,
// surface, and platform, which must not import one another. It is the
// canonical home for the string/hash primitives that were previously
// duplicated between tools/core and capability/memory/core.
package strutil

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// FirstNonEmpty returns the first value whose trimmed form is non-empty,
// returned verbatim, or "" if none qualify.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// FirstNonEmptyTrimmed behaves like FirstNonEmpty but returns the chosen value
// with surrounding whitespace trimmed.
func FirstNonEmptyTrimmed(values ...string) string {
	return strings.TrimSpace(FirstNonEmpty(values...))
}

// TruncateUTF8 truncates s to at most max bytes, backing off so the result
// stays valid UTF-8.
func TruncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// HashHex returns the hex-encoded SHA-256 digest of s.
func HashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// HashHexTrimmed returns the hex-encoded SHA-256 digest of the trimmed s. It is
// used where cache keys, dedup hashes, and audit hashes must be insensitive to
// surrounding whitespace.
func HashHexTrimmed(s string) string { return HashHex(strings.TrimSpace(s)) }

// ExtractJSONObject extracts the first JSON object after optional prose/fences.
// Invalid or incomplete input remains unchanged so callers reject it on decode.
func ExtractJSONObject(raw string) string {
	start := strings.IndexByte(raw, '{')
	if start < 0 {
		return raw
	}
	var object json.RawMessage
	if err := json.NewDecoder(strings.NewReader(raw[start:])).Decode(&object); err != nil {
		return raw
	}
	return string(object)
}
