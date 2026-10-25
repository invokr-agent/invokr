package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/config"
)

func TestRenderProfilesMarksPersistedMarkdownProfile(t *testing.T) {
	app := &config.AppConfig{Profile: "terse"}
	app.Provider.Router = config.RouterConfig{Active: "local",
		Providers: map[string]config.RouterProviderConfig{"local": {Type: "openai-compatible"}},
		Profiles:  map[string]config.RouterProfileConfig{"local": {Provider: "local", Model: "m"}}}
	var out bytes.Buffer
	if err := renderProfiles(&out, app, []string{"terse"}); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "*") != strings.Contains(line, "terse") {
			t.Fatalf("active marker on the wrong row:\n%s", out.String())
		}
	}
}

func TestProfileNameCollisionPrefersMarkdown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("INVOKR_HOME", home)
	if err := os.MkdirAll(config.ProfilesDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.ProfilesDir(home), "x.md"), []byte("Be terse.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgJSON := `{"provider":{"router":{"active":"x","providers":{"p":{"type":"openai-compatible"}},"profiles":{"x":{"provider":"p","model":"router-model"}}}}}`
	if err := os.WriteFile(config.DefaultConfigPath(), []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	app := &config.AppConfig{}
	app.Provider.Router = config.RouterConfig{Active: "x",
		Providers: map[string]config.RouterProviderConfig{"p": {Type: "openai-compatible"}},
		Profiles:  map[string]config.RouterProfileConfig{"x": {Provider: "p", Model: "router-model"}}}
	var out bytes.Buffer
	if err := renderProfiles(&out, app, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "router-model") || !strings.Contains(out.String(), "file") {
		t.Fatalf("profiles table shows the shadowed router profile:\n%s", out.String())
	}
	use := newUseProfileCmd(app)
	out.Reset()
	use.SetOut(&out)
	use.SetArgs([]string{"x"})
	if err := use.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "router-model") {
		t.Fatalf("config use printed router details: %q", out.String())
	}
}

func TestConfigShowPrintsReadableDurations(t *testing.T) {
	app := &config.AppConfig{}
	app.Provider.HTTPTimeout = 10 * time.Minute
	cmd := NewConfigCmd(app)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config show: %v", err)
	}
	if !strings.Contains(out.String(), `"http_timeout": "10m0s"`) {
		t.Fatalf("expected readable http_timeout, got %s", out.String())
	}
}
