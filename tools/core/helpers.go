package core

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/support/strutil"
)

// MarshalResult JSON-encodes a tool result, wrapping any encode error with a
// message identifying the tool: "encode <toolName> result: %w".
func MarshalResult(toolName string, v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode %s result: %w", toolName, err)
	}
	return string(data), nil
}

// Outcome maps an error to the audit outcome string: "failed" when err is
// non-nil, "completed" otherwise.
func Outcome(err error) string {
	if err != nil {
		return "failed"
	}
	return "completed"
}

// FirstNonEmpty returns the first value whose trimmed form is non-empty,
// returned verbatim, or "" if none qualify. Re-exported from support/strutil
// so tool packages keep a single import for the common helpers.
func FirstNonEmpty(values ...string) string { return strutil.FirstNonEmpty(values...) }

// FirstNonEmptyTrimmed behaves like FirstNonEmpty but returns the chosen value
// with surrounding whitespace trimmed.
func FirstNonEmptyTrimmed(values ...string) string { return strutil.FirstNonEmptyTrimmed(values...) }

// CanonicalArgsJSON normalizes tool-call arguments so semantically identical
// calls that differ only in whitespace or key ordering produce the same string.
// Invalid JSON falls back to the raw bytes. It is the canonicalization behind
// the runner's repetition-guard key.
func CanonicalArgsJSON(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	canon, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(canon)
}

// CallHash is the stable identity of a tool call (name + canonical arguments).
func CallHash(name string, args []byte) string {
	return HashHex(name + "\x00" + CanonicalArgsJSON(args))
}

// HashHex returns the hex-encoded SHA-256 digest of s. Re-exported from
// support/strutil.
func HashHex(s string) string { return strutil.HashHex(s) }

// HashHexTrimmed returns the hex-encoded SHA-256 digest of the trimmed s. It is
// used where cache keys/audit hashes must be insensitive to surrounding
// whitespace.
func HashHexTrimmed(s string) string { return strutil.HashHexTrimmed(s) }

// WithEnvValue returns a copy of env with key set to value, replacing an
// existing KEY=value entry or appending a new one. The input slice is never
// mutated.
func WithEnvValue(env []string, key string, value string) []string {
	prefix := key + "="
	res := append([]string(nil), env...)
	for i, item := range res {
		if strings.HasPrefix(item, prefix) {
			res[i] = prefix + value
			return res
		}
	}
	return append(res, prefix+value)
}

// GetEnvValue returns the value of key in env, or "" if not present.
func GetEnvValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if val, ok := strings.CutPrefix(item, prefix); ok {
			return val
		}
	}
	return ""
}

// DedupPreserveOrder returns values with duplicates removed, keeping the
// position of each value's first occurrence.
func DedupPreserveOrder(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// TruncateRunes cuts s to at most limit runes, without appending any suffix.
// A limit <= 0 returns "".
func TruncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == limit {
			return s[:i]
		}
		count++
	}
	return s
}

// DispatchAudit emits an audit event, wiring the session ID from ctx and the
// error string from err. It is a no-op when dispatcher is nil.
func DispatchAudit(ctx context.Context, dispatcher events.Dispatcher, payload *events.AuditPayload, err error) {
	events.Audit(ctx, dispatcher, SessionIDFromContext(ctx), payload, err)
}

// NormalizeStrings trims, drops blanks and duplicates, and sorts — the shared
// front half of every tool allowlist. Pass lowercase for the callers that
// match case-insensitively.
func NormalizeStrings(values []string, lowercase bool) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); lowercase {
			value = strings.ToLower(value)
		}
		if value != "" {
			out = append(out, value)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// StringSet turns a slice into a membership set, ignoring blanks.
func StringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	return set
}

// AuditDetails emits an audit event for tool with the given outcome and detail
// map, using the tool name for both Action and Tool.
func AuditDetails(ctx context.Context, dispatcher events.Dispatcher, tool, outcome string, details map[string]any, err error) {
	DispatchAudit(ctx, dispatcher, &events.AuditPayload{Action: tool, Tool: tool, Outcome: outcome, Details: details}, err)
}

// MapSlice returns the result of applying f to each element of in.
func MapSlice[T, R any](in []T, f func(T) R) []R {
	out := make([]R, 0, len(in))
	for _, item := range in {
		out = append(out, f(item))
	}
	return out
}
