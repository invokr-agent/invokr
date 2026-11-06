package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/invokr-agent/invokr/support/fsutil"
)

// GitIdentityConfig is invokr's own git/GitHub identity, stored under the Invokr
// home so git and gh children can act as a separate account without touching
// the host's config. Only `invokr setup git-identity` writes it: a GIT_CONFIG_*
// value can name a binary git executes, so nothing the agent chooses may reach
// these entries.
type GitIdentityConfig struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	// SigningKey is the path of the SSH key git signs commits with; empty means
	// no signing.
	SigningKey string `json:"signing_key"`
}

func GitIdentityPath(homeDir string) string { return filepath.Join(homeDir, "git-identity.json") }

// GitIdentityDir is the gh config dir GH_CONFIG_DIR points at, keeping the bot's
// GitHub login apart from the host's ~/.config/gh.
func GitIdentityDir(homeDir string) string { return filepath.Join(homeDir, "gh") }

// LoadGitIdentity reads the stored identity; no file means no identity, so an
// unconfigured run behaves exactly as it did before.
func LoadGitIdentity(homeDir string) (GitIdentityConfig, error) {
	identity, _, err := fsutil.ReadJSON[GitIdentityConfig](GitIdentityPath(homeDir))
	if err != nil {
		return GitIdentityConfig{}, err
	}
	return identity, nil
}

// SaveGitIdentity writes the identity 0600 under a 0700 home, atomically.
func SaveGitIdentity(identity GitIdentityConfig, homeDir string) error {
	return fsutil.SaveJSON(GitIdentityPath(homeDir), identity)
}

// ApplyGitIdentity loads the stored identity and puts its variables in this
// process's environment, before any tool runs, so every git and gh child
// inherits them. Nothing stored sets nothing. An unparseable file is an error
// rather than a silent fall back to the host's identity: running as the wrong
// account is worse than refusing to start.
func ApplyGitIdentity(homeDir string) error {
	identity, err := LoadGitIdentity(homeDir)
	if err != nil {
		return err
	}
	for _, entry := range GitIdentityEnv(identity, homeDir) {
		key, value, _ := strings.Cut(entry, "=")
		os.Setenv(key, value)
	}
	return nil
}

// ClearGitIdentity drops the stored identity, its signing settings and the gh
// login. Nothing configured is not an error, so --reset is idempotent.
func ClearGitIdentity(homeDir string) error {
	if err := os.Remove(GitIdentityPath(homeDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.RemoveAll(GitIdentityDir(homeDir))
}

// GitIdentityEnv is the environment every git and gh child inherits. Environment
// beats repo and global git config, so a repository that pins its own identity
// is still committed, signed and pushed as the configured account; env also
// writes no file to the host config.
//
// GIT_CONFIG_* carries keys the way `git -c` does. An empty credential.helper
// first drops the host's helpers, then gh serves the bot's login over https,
// and ssh-style remotes are rewritten to https so the same login authorizes them.
func GitIdentityEnv(identity GitIdentityConfig, homeDir string) []string {
	if identity.Name == "" || identity.Email == "" {
		return nil
	}
	env := []string{
		"GIT_AUTHOR_NAME=" + identity.Name, "GIT_AUTHOR_EMAIL=" + identity.Email,
		"GIT_COMMITTER_NAME=" + identity.Name, "GIT_COMMITTER_EMAIL=" + identity.Email,
		"GH_CONFIG_DIR=" + GitIdentityDir(homeDir),
	}
	keys := []string{"credential.helper", "credential.https://github.com.helper", "url.https://github.com/.insteadOf"}
	values := []string{"", "!gh auth git-credential", "git@github.com:"}
	if identity.SigningKey != "" {
		keys = append(keys, "gpg.format", "user.signingkey", "commit.gpgsign")
		values = append(values, "ssh", identity.SigningKey, "true")
	}
	env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(keys)))
	for i := 0; i < len(keys); i++ {
		env = append(env,
			"GIT_CONFIG_KEY_"+strconv.Itoa(i)+"="+keys[i],
			"GIT_CONFIG_VALUE_"+strconv.Itoa(i)+"="+values[i],
		)
	}
	return env
}

// GitIdentityEnvNames are the names GitIdentityEnv can set. The shell sandbox
// and the docker relaunch forward these names only, so the isolation that keeps
// GIT_DIR, GIT_SSH_COMMAND and friends out of a sandboxed command still holds.
func GitIdentityEnvNames() []string {
	names := []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME",
		"GIT_COMMITTER_EMAIL", "GH_CONFIG_DIR", "GIT_CONFIG_COUNT"}
	for i := 0; i < 6; i++ {
		names = append(names, "GIT_CONFIG_KEY_"+strconv.Itoa(i), "GIT_CONFIG_VALUE_"+strconv.Itoa(i))
	}
	return names
}
