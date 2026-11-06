package shell

import (
	"os"
	"slices"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/fsutil"
)

// git signs a commit inside the sandbox only if it can read the key user.signingkey
// names. The Invokr home that holds that copy also holds the gh login and the stored
// identity, so the plan mounts the one key file and nothing else (#103).
func TestSandboxMountsOnlyTheSigningKey(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	key := config.GitIdentityKeyPath(home)
	if err := os.WriteFile(key, []byte("key material"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := fsutil.SaveJSON(config.GitIdentityPath(home),
		config.GitIdentityConfig{Name: "octocat-bot", Email: "octocat-bot@example.com", SigningKey: config.GitIdentityKeyFileName}); err != nil {
		t.Fatalf("save identity: %v", err)
	}
	t.Setenv("INVOKR_HOME", home)
	plan := preparedShellCommand{workspaceRoot: ws, cwd: ws, mode: approval.Manual,
		commands: []string{"git"}, line: "git commit -m x"}
	sb := sandboxPlan("/bin/bash", plan)
	if !slices.Contains(sb.RO, key) {
		t.Fatalf("%s is not mounted read-only: %v", key, sb.RO)
	}
	for _, mounted := range slices.Concat(sb.RO, sb.RW, sb.Sealed, sb.PinnedRO) {
		for _, broad := range []string{home, config.GitIdentityDir(home), config.GitIdentityPath(home)} {
			if mounted == broad {
				t.Fatalf("%s exposes more of the Invokr home than the key", mounted)
			}
		}
	}
}

// An identity that does not sign adds no key mount: the sandbox stays as it was.
func TestSandboxMountsNoKeyWithoutSigning(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	if err := fsutil.SaveJSON(config.GitIdentityPath(home),
		config.GitIdentityConfig{Name: "octocat-bot", Email: "octocat-bot@example.com"}); err != nil {
		t.Fatalf("save identity: %v", err)
	}
	t.Setenv("INVOKR_HOME", home)
	plan := preparedShellCommand{workspaceRoot: ws, cwd: ws, mode: approval.Manual,
		commands: []string{"git"}, line: "git commit -m x"}
	sb := sandboxPlan("/bin/bash", plan)
	if slices.Contains(sb.RO, config.GitIdentityKeyPath(home)) {
		t.Fatalf("an identity with no signing key mounted the key file: %v", sb.RO)
	}
}
