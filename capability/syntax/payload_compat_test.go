package syntax

import (
	"errors"
	"strings"
	"testing"
)

// The envelopes keep the bytes the earlier string-spliced versions produced:
// key order, compact form, and json.Marshal's escaping of quotes, HTML and
// control characters.
func TestPayloadsKeepTheirWireForm(t *testing.T) {
	err := errors.New("bad \"q\" <t> & \\ \n\x01")
	const escaped = `"bad \"q\" \u003ct\u003e \u0026 \\ \n\u0001"`
	guidance := `"` + strings.ReplaceAll(deniedGuidance, `"`, `\"`) + `"`
	for got, want := range map[string]string{
		ErrorPayload(err):        `{"error":` + escaped + `}`,
		DeniedPayload(err):       `{"error":` + escaped + `,"denied":true,"guidance":` + guidance + `}`,
		DeniedRetryPayload("ls"): `{"error":"approval already denied in this request for ls with these exact arguments","denied":true,"guidance":` + guidance + `}`,
		ErrorPayload(nil):        `{}`,
		DeniedPayload(nil):       `{}`,
	} {
		if got != want {
			t.Fatalf("got  %s\nwant %s", got, want)
		}
	}
}
