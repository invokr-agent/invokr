package setup

import (
	"fmt"
	"io"
	"os"
	"os/exec"
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
