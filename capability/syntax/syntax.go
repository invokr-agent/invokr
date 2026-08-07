package syntax

import (
	"encoding/json"
)

func quoteJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `"unknown error"`
	}
	return string(b)
}

func ErrorPayload(err error) string {
	if err == nil {
		return "{}"
	}
	return `{"error":` + quoteJSON(err.Error()) + `}`
}

const deniedGuidance = "The user denied this action. Do not request the identical action again; change your approach or ask the user how to proceed."

// DeniedPayload is the tool result for an approval denial. Unlike a plain
// error payload it tells the model the denial is final, so it does not
// immediately re-request the same action.
func DeniedPayload(err error) string {
	if err == nil {
		return "{}"
	}
	return `{"error":` + quoteJSON(err.Error()) + `,"denied":true,"guidance":` + quoteJSON(deniedGuidance) + `}`
}

// DeniedRetryPayload is returned without executing anything when the model
// re-issues a call the user already denied in this request.
func DeniedRetryPayload(tool string) string {
	return `{"error":` + quoteJSON("approval already denied for "+tool+" with these exact arguments") + `,"denied":true,"guidance":` + quoteJSON(deniedGuidance) + `}`
}
