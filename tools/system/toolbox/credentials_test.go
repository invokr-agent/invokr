package toolbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools/core"
)

// credentialEcho returns whatever credentials arrived on stdin.
const credentialEcho = `import json, sys
call = json.load(sys.stdin)
print(json.dumps({"result": json.dumps(call.get("credentials"))}))`

func stubGHToken(t *testing.T, token string, err error) *int {
	t.Helper()
	calls := 0
	prev := GHToken
	GHToken = func(context.Context) (string, error) { calls++; return token, err }
	t.Cleanup(func() { GHToken = prev })
	return &calls
}

func TestCredentialsFromGHReachToolOnStdin(t *testing.T) {
	calls := stubGHToken(t, "gho_TEST", nil)
	root := t.TempDir()
	writePlugin(t, root, "cred_tool", credentialEcho, map[string]any{"credentials": map[string]any{"token": map[string]string{"source": "gh"}}})
	handler := loadPluginHandler(t, root, "cred_tool", nil)

	out, err := handler(context.Background(), &core.ToolCall{Name: "cred_tool", Arguments: []byte(`{}`)})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if out != `{"token": "gho_TEST"}` {
		t.Fatalf("credentials on stdin = %s", out)
	}
	if *calls != 1 {
		t.Fatalf("gh resolved %d times, want 1 per call", *calls)
	}
}

func TestToolWithoutCredentialsNeverResolvesGH(t *testing.T) {
	calls := stubGHToken(t, "gho_TEST", nil)
	root := t.TempDir()
	writePlugin(t, root, "plain_tool", credentialEcho, nil)
	handler := loadPluginHandler(t, root, "plain_tool", nil)

	out, err := handler(context.Background(), &core.ToolCall{Name: "plain_tool", Arguments: []byte(`{}`)})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if out != "null" || *calls != 0 {
		t.Fatalf("out = %s, gh calls = %d; want no credentials", out, *calls)
	}
}

func TestGHFailureStopsCallBeforeSpawn(t *testing.T) {
	stubGHToken(t, "", errors.New("gh auth token failed (run `gh auth login`)"))
	root := t.TempDir()
	writePlugin(t, root, "cred_tool", sentinelPlugin, map[string]any{"credentials": map[string]any{"token": map[string]string{"source": "gh"}}})
	handler := loadPluginHandler(t, root, "cred_tool", nil)

	_, err := handler(context.Background(), &core.ToolCall{Name: "cred_tool", Arguments: []byte(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("err = %v, want gh login hint", err)
	}
	if _, statErr := os.Stat(filepath.Join(sentinelRoot(t, root), "sentinel")); !os.IsNotExist(statErr) {
		t.Fatalf("plugin spawned without its credential: %v", statErr)
	}
}

func TestLoadRejectsUnknownCredentialSource(t *testing.T) {
	for name, creds := range map[string]any{
		"source": map[string]any{"token": map[string]string{"source": "env"}},
	} {
		dir := writePlugin(t, t.TempDir(), "cred_tool", credentialEcho, map[string]any{"credentials": creds})
		if _, err := loadToolboxTool(dir, config.ToolboxConfig{}.WithDefaults(t.TempDir()), nil); err == nil || !strings.Contains(err.Error(), "credentials.") {
			t.Fatalf("%s: err = %v, want credentials rejection", name, err)
		}
	}
}

func TestWriteToolSecretIsOwnerOnly(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "cred_tool", credentialEcho, nil)

	path, err := WriteToolSecret(root, "cred_tool", " gho_TEST\n")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v; want 0600", info.Mode().Perm(), err)
	}
	raw, _ := os.ReadFile(path)
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil || got["token"] != "gho_TEST" {
		t.Fatalf("secrets = %s, err = %v", raw, err)
	}

	for _, tc := range [][2]string{{"../cred_tool", "gho_TEST"}, {"missing", "gho_TEST"}, {"cred_tool", "has space"}, {"cred_tool", ""}} {
		if _, err := WriteToolSecret(root, tc[0], tc[1]); err == nil {
			t.Fatalf("WriteToolSecret(%q, %q) succeeded", tc[0], tc[1])
		}
	}
}
