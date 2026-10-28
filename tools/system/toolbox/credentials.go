package toolbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/invokr-agent/invokr/support/fsutil"
	"github.com/invokr-agent/invokr/tools/core"
)

// toolboxCredential is one manifest credentials entry. The value is resolved
// by invokr on the host at call time, outside the sandbox, and handed to the
// tool on stdin only: never in argv, env, logs or the model's view.
type toolboxCredential struct {
	Source string `json:"source"`
}

func validateCredentials(creds map[string]toolboxCredential) error {
	for name, cred := range creds {
		if cred.Source != "gh" {
			return fmt.Errorf("credentials.%s: unsupported source %q (supported: gh)", name, cred.Source)
		}
	}
	return nil
}

// GHToken is a var so tests can stand in for the gh CLI.
var GHToken = func(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("gh auth token failed (install gh or run gh auth login): %w", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", errors.New("gh auth token returned nothing; run `gh auth login`")
	}
	return token, nil
}

// resolveCredentials returns nil when the manifest declares none, so requestBytes
// omits the credentials key; one gh login answers for every declared name.
func resolveCredentials(ctx context.Context, creds map[string]toolboxCredential) (map[string]string, error) {
	if len(creds) == 0 {
		return nil, nil
	}
	token, err := GHToken(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(creds))
	for name := range creds {
		out[name] = token
	}
	return out, nil
}

// requestBytes is the stdin payload: the call, plus resolved credentials when
// the manifest declares any.
func (p *executableTool) requestBytes(ctx context.Context, call *core.ToolCall) ([]byte, error) {
	creds, err := resolveCredentials(ctx, p.credentials)
	if err != nil {
		return nil, fmt.Errorf("toolbox tool %q: %w", p.Name, err)
	}
	return json.Marshal(struct {
		*core.ToolCall
		Credentials map[string]string `json:"credentials,omitempty"`
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
	path := filepath.Join(dir, ".secrets.json")
	if err := fsutil.WriteFileAtomic(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}
