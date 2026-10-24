package toolbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/support/fsutil"
	"github.com/invokr-ai/invokr/tools/core"
)

// CredentialSourceGH resolves a credential with `gh auth token` on the host,
// so a tool rides the user's existing GitHub CLI login.
const CredentialSourceGH = "gh"

// SecretsFile is the owner-only credential file a tool may read from its own
// directory; `invokr toolbox auth` writes it.
const SecretsFile = ".secrets.json"

const credentialTimeout = 10 * time.Second

// toolboxCredential is one manifest credentials entry. The value is resolved
// by invokr on the host at call time, outside the sandbox, and handed to the
// tool on stdin only: never in argv, env, logs or the model's view.
type toolboxCredential struct {
	Source string `json:"source"`
}

var credentialName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

func validateCredentials(creds map[string]toolboxCredential) error {
	for name, cred := range creds {
		if !credentialName.MatchString(name) {
			return fmt.Errorf("credentials.%s: name must match %s", name, credentialName)
		}
		if cred.Source != CredentialSourceGH {
			return fmt.Errorf("credentials.%s: unsupported source %q (supported: %s)", name, cred.Source, CredentialSourceGH)
		}
	}
	return nil
}

// ghToken is a var so tests can stand in for the gh CLI.
var ghToken = func(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, credentialTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("gh CLI not found; install it or run `invokr toolbox auth <tool>` with a token")
		}
		return "", fmt.Errorf("gh auth token failed (run `gh auth login`): %w", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", errors.New("gh auth token returned nothing; run `gh auth login`")
	}
	return token, nil
}

// GHToken returns the token of the user's current GitHub CLI login.
func GHToken(ctx context.Context) (string, error) { return ghToken(ctx) }

func resolveCredentials(ctx context.Context, creds map[string]toolboxCredential) (map[string]string, error) {
	out := make(map[string]string, len(creds))
	for name := range creds {
		// Only gh exists today; validateCredentials refused anything else at load.
		token, err := ghToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("credential %q: %w", name, err)
		}
		out[name] = token
	}
	return out, nil
}

// requestBytes is the stdin payload: the call, plus resolved credentials when
// the manifest declares any.
func (p *executableTool) requestBytes(ctx context.Context, call *core.ToolCall) ([]byte, error) {
	if len(p.credentials) == 0 {
		b, err := json.Marshal(call)
		if err != nil {
			return nil, fmt.Errorf("serialize tool call: %w", err)
		}
		return b, nil
	}
	creds, err := resolveCredentials(ctx, p.credentials)
	if err != nil {
		return nil, fmt.Errorf("toolbox tool %q: %w", p.Name, err)
	}
	return json.Marshal(struct {
		*core.ToolCall
		Credentials map[string]string `json:"credentials"`
	}{call, creds})
}

// WriteToolSecret stores token as {"token": ...} in the tool's owner-only
// secrets file, replacing any previous one atomically.
func WriteToolSecret(toolboxDir, tool, token string) (string, error) {
	if tool == "" || tool != filepath.Base(tool) || strings.HasPrefix(tool, ".") {
		return "", fmt.Errorf("invalid tool name %q", tool)
	}
	dir := filepath.Join(toolboxDir, tool)
	if _, err := findToolboxSpecFile(dir); err != nil {
		return "", fmt.Errorf("tool %q is not installed: %w", tool, err)
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsFunc(token, func(r rune) bool { return r < 33 || r > 126 }) {
		return "", errors.New("token must be printable ASCII without whitespace")
	}
	data, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, SecretsFile)
	if err := fsutil.WriteFileAtomic(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, os.Chmod(path, 0o600)
}
