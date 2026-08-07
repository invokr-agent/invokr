package agent

import (
	"encoding/json"
	"fmt"
)

// capToolResult enforces a hard context-facing cap: whatever a tool buffered
// internally, at most maxTokens estimated tokens of it enter the conversation.
// It is the result gate's lossy fallback. Uses the same len/4 byte
// heuristic as estimatedMessageTokenCount so the cap and the context budget
// agree on the estimate.
//
// Tools that marshal a JSON envelope (shell, code_interpreter, toolbox) get the
// long string fields inside it trimmed, so the model keeps exit_code, timed_out
// and artifacts even when stdout alone blows the budget. Anything else is
// truncated on a UTF-8 boundary. Both paths end with a marker.
func capToolResult(output string, maxTokens int) string {
	limit := maxTokens * 4 // len/4 estimator, inverted
	if len(output) <= limit {
		return output
	}
	if capped, ok := capJSONEnvelope(output, limit); ok {
		return capped
	}
	return truncateWithMarker(output, limit)
}

// truncationMarker tells the model the result was cut and what to do about it.
func truncationMarker(limit int) string {
	return fmt.Sprintf(
		"\n[output truncated at ~%d tokens — narrow the request or fetch it in smaller parts]", limit/4)
}

// truncateWithMarker cuts s to at most limit bytes on a UTF-8 boundary and
// appends the marker. Returns s unchanged when it already fits.
func truncateWithMarker(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:runeFloor(s, limit)] + truncationMarker(limit)
}

// capJSONEnvelope trims the longest string fields of a JSON object result until
// the whole envelope fits, keeping every key the tool reported. Reports false
// when output is not a JSON object or trimming cannot get it under the limit,
// leaving the caller on the plain-truncation path.
func capJSONEnvelope(output string, limit int) (string, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &fields); err != nil || len(fields) == 0 {
		return "", false
	}
	// Re-marshalling can re-escape, so aim below the limit for headroom.
	target := limit - limit/10
	for range fields {
		key, val, ok := longestStringField(fields)
		if !ok {
			return "", false
		}
		trimmed := truncateWithMarker(val, max(len(val)-(len(output)-target), 0))
		if trimmed == val {
			return "", false // no progress available; fall back
		}
		encoded, err := json.Marshal(trimmed)
		if err != nil {
			return "", false
		}
		fields[key] = encoded
		b, err := json.Marshal(fields)
		if err != nil {
			return "", false
		}
		output = string(b)
		if len(output) <= limit {
			return output, true
		}
	}
	return "", false
}

// longestStringField returns the key and decoded value of the longest string
// field, so the trim loop always spends its budget on the biggest offender.
func longestStringField(fields map[string]json.RawMessage) (string, string, bool) {
	var bestKey, bestVal string
	found := false
	for key, raw := range fields {
		var val string
		if err := json.Unmarshal(raw, &val); err != nil {
			continue
		}
		if found && len(val) <= len(bestVal) {
			continue
		}
		bestKey, bestVal, found = key, val, true
	}
	return bestKey, bestVal, found
}
