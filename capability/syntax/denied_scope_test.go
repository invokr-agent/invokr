package syntax

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDenialGuidanceIsRequestScoped(t *testing.T) {
	for _, payload := range []string{DeniedPayload(errors.New("edit denied")), DeniedRetryPayload("edit")} {
		var result struct {
			Denied   bool
			Guidance string
		}
		if err := json.Unmarshal([]byte(payload), &result); err != nil {
			t.Fatal(err)
		}
		if !result.Denied {
			t.Fatal("denial flag missing")
		}
		for _, want := range []string{"current request", "Do not retry it or work around the denial", "not a permanent ban", "new user request"} {
			if !strings.Contains(result.Guidance, want) {
				t.Errorf("guidance %q lacks %q", result.Guidance, want)
			}
		}
	}
}
