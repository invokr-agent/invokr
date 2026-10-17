package syntax

import (
	"encoding/json"
)

func quoteJSON(s string) string {
	b, _ := json.Marshal(s) // Strings always have a JSON encoding.
	return string(b)
}

func ErrorPayload(err error) string {
	if err == nil {
		return "{}"
	}
	return `{"error":` + quoteJSON(err.Error()) + `}`
}

const deniedGuidance = "The user denied this action for the current request. Do not retry it or work around the denial in this request. This is not a permanent ban: a new user request can ask for approval again. Report the denied action accurately and ask the user how to proceed."

// DeniedPayload is the tool result for an approval denial. Unlike a plain
// error payload it tells the model the denial applies to this request, so it does not
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
	return `{"error":` + quoteJSON("approval already denied in this request for "+tool+" with these exact arguments") + `,"denied":true,"guidance":` + quoteJSON(deniedGuidance) + `}`
}
