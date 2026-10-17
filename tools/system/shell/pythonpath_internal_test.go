package shell

import (
	"strings"
	"testing"
)

// PYTHONPATH names where an interpreter finds libraries, not which program
// runs. Refusing it outright left no way to run a workspace script against the
// workspace package (r9 L1/1).
func TestPolicyAdmitsALibraryPathPrefixAtTheWriteTier(t *testing.T) {
	for _, line := range []string{
		"PYTHONPATH=. python3 verify.py",
		"PYTHONPATH=. go test ./...",
		"PERL5LIB=lib go test ./...",
	} {
		pol, err := classify(t, line)
		if err != nil {
			t.Fatalf("%s refused: %v", line, err)
		}
		if pol.tier != shellTierWrite {
			t.Fatalf("%s classified %q, want the write tier", line, pol.tier)
		}
	}
}

// The allowance is scoped to one command: a bare assignment outlives the line,
// and a name that picks the program itself is still refused.
func TestPolicyStillRefusesLoaderVariablesItCannotScope(t *testing.T) {
	for _, line := range []string{
		"PYTHONPATH=.",
		"export PYTHONPATH=.",
		"PATH=/tmp go test ./...",
		"PYTHONSTARTUP=x.py go test ./...",
	} {
		if _, err := classify(t, line); err == nil || !strings.Contains(err.Error(), "is not allowed") {
			t.Fatalf("%s was admitted (err=%v)", line, err)
		}
	}
}
