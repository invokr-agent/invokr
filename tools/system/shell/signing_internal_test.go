package shell

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/fsutil"
)

// git signs a commit inside the sandbox only if it can read the key user.signingkey
// names, and the Invokr home that holds the copy also holds the gh login: mount
// the two key files, never the home or the key dir (#103).
func TestSandboxMountsTheSigningKeyAndItsPublicHalf(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(config.GitIdentityKeyDir(home), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	key := filepath.Join(config.GitIdentityKeyDir(home), "id_ed25519_signer")
	for _, path := range []string{key, key + ".pub"} {
		if err := os.WriteFile(path, []byte("key material"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if err := fsutil.SaveJSON(config.GitIdentityPath(home),
		config.GitIdentityConfig{Name: "octocat-bot", Email: "octocat-bot@example.com", SigningKey: key}); err != nil {
		t.Fatalf("save identity: %v", err)
	}
	t.Setenv("INVOKR_HOME", home)
	plan := preparedShellCommand{workspaceRoot: ws, cwd: ws, mode: approval.Manual,
		commands: []string{"git"}, line: "git commit -m x"}
	sb := sandboxPlan("/bin/bash", plan)
	for _, path := range []string{key, key + ".pub"} {
		if !slices.Contains(sb.RO, path) {
			t.Fatalf("%s is not mounted read-only: %v", path, sb.RO)
		}
	}
	for _, mounted := range slices.Concat(sb.RO, sb.RW, sb.Sealed, sb.PinnedRO) {
		for _, broad := range []string{home, config.GitIdentityKeyDir(home), config.GitIdentityDir(home)} {
			if mounted == broad {
				t.Fatalf("%s exposes more of the Invokr home than the key", mounted)
			}
		}
	}
}

// An identity that does not sign adds no mount: the sandbox stays as it was.
func TestSigningKeyFilesEmptyWithoutSigning(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	if got := signingKeyFiles(); len(got) != 0 {
		t.Fatalf("an identity with no signing key mounted %v", got)
	}
}
