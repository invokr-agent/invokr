package shell

import (
	"strings"
	"testing"
)

func TestTimeoutHintDiscouragesBlindRetry(t *testing.T) {
	hint := timeoutHint(30000)
	for _, want := range []string{"all its child processes", "runaway children", "inspect the program", "re-executes itself", "legitimately slow"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint missing %q: %s", want, hint)
		}
	}
	if strings.Contains(hint, "for longer work") {
		t.Fatalf("hint still nudges a longer timeout: %s", hint)
	}
}
