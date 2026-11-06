package setup

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"charm.land/huh/v2"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/fsutil"
)

// RunGitIdentityStep asks for invokr's own git/GitHub identity and stores it
// under the Invokr home, from where the runtime applies it to every git and gh
// child. It is opt-in and asked by `invokr setup git-identity` only: the first-run
// questionnaire never asks it, so an unconfigured run uses the host's git and gh
// exactly as before.
func RunGitIdentityStep(in io.Reader, out io.Writer, homeDir string) (config.GitIdentityConfig, error) {
	identity, login, loginAnswer := config.GitIdentityConfig{}, false, ""
	accessible := accessibleMode(fsutil.IsTerminal(in))
	var piped *byteReader
	if accessible {
		// byteReader keeps the later fields' answers readable (see runQuestionnaireHuh).
		piped = &byteReader{r: in}
		in = piped
	}
	form := newForm(in, out, accessible, huh.NewGroup(
		question("Git name for invokr", "The author every invokr commit and push is filed under.", &identity.Name, accessible),
		question("Git email for invokr", "It is written into the commit, so use that account's own address.", &identity.Email, accessible),
		question("Path to an SSH signing key", "Empty means no signing; a key must need no passphrase.", &identity.SigningKey, accessible),
		yesNoQuestion(accessible, "Log in to GitHub as this account?", "Runs `gh auth login` with GH_CONFIG_DIR under the Invokr home.", &login, &loginAnswer),
	))
	if err := runForm(form, accessible); err != nil {
		return config.GitIdentityConfig{}, err
	}
	if accessible {
		login = parseYes(loginAnswer)
	}
	identity.Name, identity.Email = strings.TrimSpace(identity.Name), strings.TrimSpace(identity.Email)
	identity.SigningKey = strings.TrimSpace(identity.SigningKey)
	// A blank answer is also how a truncated pipe shows up, so refusing here
	// keeps a half-stored identity from silently changing who commits.
	if identity.Name == "" || identity.Email == "" {
		return config.GitIdentityConfig{}, fmt.Errorf("a git identity needs a name and an email; `invokr setup git-identity --reset` clears what is stored")
	}
	if identity.SigningKey != "" {
		// Store the copy under the Invokr home, not the answered path: a sandboxed
		// commit can only sign with a key the sandbox mounts, and the sandbox mounts
		// just this key dir's two files. The host's own ~/.ssh is never written.
		key, err := storeSigningKey(identity.SigningKey, homeDir)
		if err != nil {
			return config.GitIdentityConfig{}, fmt.Errorf("%s; `invokr setup git-identity --reset` clears what is stored", err)
		}
		identity.SigningKey = key
	}
	if err := fsutil.SaveJSON(config.GitIdentityPath(homeDir), identity); err != nil {
		return config.GitIdentityConfig{}, err
	}
	if !login {
		return identity, nil
	}
	if err := loginGitHub(homeDir); err != nil {
		return identity, fmt.Errorf("%s; the identity itself is stored at %s", err, config.GitIdentityPath(homeDir))
	}
	return identity, nil
}

// loginGitHub hands the terminal to `gh auth login` with gh's config dir set to
// invokr's own, so the bot's GitHub login stays apart from the host's
// ~/.config/gh. The stdios are attached rather than piped: gh opens a browser
// and reads its device-code prompt from the user's own terminal.
func loginGitHub(homeDir string) error {
	gh := exec.Command("gh", "auth", "login")
	gh.Env = append(os.Environ(), "GH_CONFIG_DIR="+config.GitIdentityDir(homeDir))
	gh.Stdin, gh.Stdout, gh.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := gh.Run(); err != nil {
		return fmt.Errorf("`gh auth login` exited before the login finished: %w", err)
	}
	return nil
}

// storeSigningKey copies the answered key, and its public half when that exists,
// into invokr's own key dir: dir 0700, files 0600. The copy is what lets a
// sandboxed commit sign without exposing the rest of the Invokr home, and the
// key must need no passphrase because no ssh-agent runs inside the sandbox.
func storeSigningKey(source, homeDir string) (string, error) {
	dir := config.GitIdentityKeyDir(homeDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// MkdirAll applies the umask, and a key dir another account can write is a
	// key another account can swap.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	name := filepath.Join(dir, filepath.Base(source))
	if err := fsutil.CopyFile(source, name, 0o600); err != nil {
		return "", fmt.Errorf("%s: %w", source, err)
	}
	// git derives the public half from the private key when it has to, so a
	// missing .pub is not a reason to refuse the identity.
	if _, err := os.Stat(source + ".pub"); err == nil {
		if err := fsutil.CopyFile(source+".pub", name+".pub", 0o600); err != nil {
			return "", fmt.Errorf("%s.pub: %w", source, err)
		}
	}
	return name, nil
}
