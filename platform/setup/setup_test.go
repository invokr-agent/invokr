package setup

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
)

func TestEnsureAtCreatesHomeAndDefaultConfig(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".invokr")
	t.Setenv("INVOKR_HOME", home)

	result, err := EnsureAt(home)
	if err != nil {
		t.Fatalf("EnsureAt returned error: %v", err)
	}

	if !result.HomeCreated {
		t.Fatal("expected home directory to be created")
	}
	if !result.ConfigCreated {
		t.Fatal("expected config file to be created")
	}
	if result.HomeDir != home {
		t.Fatalf("expected result home %q, got %q", home, result.HomeDir)
	}
	if result.ConfigPath != filepath.Join(home, constants.DefaultConfigFile) {
		t.Fatalf("expected config path under home, got %q", result.ConfigPath)
	}
	if result.PersonaPath != filepath.Join(home, constants.PersonaFilename) {
		t.Fatalf("expected persona path under home, got %q", result.PersonaPath)
	}

	info, err := os.Stat(home)
	if err != nil {
		t.Fatalf("expected home dir to exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected home path to be a directory")
	}

	data, err := os.ReadFile(result.ConfigPath)
	if err != nil {
		t.Fatalf("expected config file to exist: %v", err)
	}

	var cfg config.AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("expected valid config JSON: %v", err)
	}
	if cfg.Provider.BaseURL != constants.DefaultProviderBaseURL {
		t.Fatalf("expected default base URL in config, got %q", cfg.Provider.BaseURL)
	}
	for _, dir := range []string{DefaultToolboxDir, constants.DefaultSkillsDir} {
		if info, err := os.Stat(filepath.Join(home, dir)); err != nil {
			t.Fatalf("expected %s directory to exist: %v", dir, err)
		} else if !info.IsDir() {
			t.Fatalf("expected %s to be a directory", dir)
		}
	}
}

func TestEnsureHomeInstallsNoToolboxDefaults(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".invokr")
	if _, err := EnsureAt(home); err != nil {
		t.Fatalf("EnsureAt returned error: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(home, DefaultToolboxDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty toolbox dir, got %v", entries)
	}
}

func TestEnsureAtDoesNotOverwriteExistingConfig(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, constants.DefaultConfigFile)
	original := []byte(`{"provider":{"base_url":"http://custom.local:8080"}}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := EnsureAt(home)
	if err != nil {
		t.Fatalf("EnsureAt returned error: %v", err)
	}
	if result.HomeCreated {
		t.Fatal("did not expect existing home to be recreated")
	}
	if result.ConfigCreated {
		t.Fatal("did not expect existing config to be recreated")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Fatalf("expected existing config to be preserved, got %q", string(data))
	}
}

func TestEnsureAtRepairsMissingConfigInExistingHome(t *testing.T) {
	home := t.TempDir()

	result, err := EnsureAt(home)
	if err != nil {
		t.Fatalf("EnsureAt returned error: %v", err)
	}
	if result.HomeCreated {
		t.Fatal("did not expect existing home to be recreated")
	}
	if !result.ConfigCreated {
		t.Fatal("expected missing config to be created")
	}
	if _, err := os.Stat(result.ConfigPath); err != nil {
		t.Fatalf("expected config to exist: %v", err)
	}
	for _, dir := range []string{DefaultToolboxDir, constants.DefaultSkillsDir} {
		if info, err := os.Stat(filepath.Join(home, dir)); err != nil {
			t.Fatalf("expected %s directory to exist: %v", dir, err)
		} else if !info.IsDir() {
			t.Fatalf("expected %s to be a directory", dir)
		}
	}
}

func TestEnsureAtRejectsExistingNonDirectoryHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".invokr")
	if err := os.WriteFile(home, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := EnsureAt(home)
	if err == nil {
		t.Fatal("expected existing file home path to return an error")
	}
	if !strings.Contains(err.Error(), "exists but is not a directory") {
		t.Fatalf("expected non-directory error, got %v", err)
	}
}

func TestEnsureAtDefaultConfigUsesExplicitHomeInsteadOfInvokrHomeEnv(t *testing.T) {
	envHome := filepath.Join(t.TempDir(), "env-home")
	selectedHome := filepath.Join(t.TempDir(), "selected-home")
	t.Setenv("INVOKR_HOME", envHome)

	result, err := EnsureAt(selectedHome)
	if err != nil {
		t.Fatalf("EnsureAt returned error: %v", err)
	}

	cfg := readConfig(t, result.ConfigPath)
	if cfg.Memory.Dir != filepath.Join(selectedHome) {
		t.Fatalf("expected memory dir under selected home, got %q", cfg.Memory.Dir)
	}
}

func TestEnsureUsesInvokrHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "custom-home")
	t.Setenv("INVOKR_HOME", home)

	result, err := Ensure()
	if err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	if result.HomeDir != home {
		t.Fatalf("expected INVOKR_HOME %q, got %q", home, result.HomeDir)
	}
	if !result.HomeCreated {
		t.Fatal("expected INVOKR_HOME directory to be created")
	}
}

func TestPathsForDocumentsFutureHomeTargets(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".invokr")

	paths := PathsFor(home)

	assertPath := func(name, got, want string) {
		t.Helper()
		if got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}

	assertPath("HomeDir", paths.HomeDir, home)
	assertPath("ConfigPath", paths.ConfigPath, filepath.Join(home, constants.DefaultConfigFile))
	assertPath("ToolboxDir", paths.ToolboxDir, filepath.Join(home, DefaultToolboxDir))
	assertPath("SkillsDir", paths.SkillsDir, filepath.Join(home, constants.DefaultSkillsDir))
	assertPath("MemoryDir", paths.MemoryDir, filepath.Join(home))
	assertPath("PersonaPath", paths.PersonaPath, filepath.Join(home, constants.PersonaFilename))
	assertPath("UserMemoryPath", paths.UserMemoryPath, filepath.Join(home, constants.UserMemoryFilename))
	assertPath("ProjectMemoryPath", paths.ProjectMemoryPath, filepath.Join(home, constants.ProjectMemoryFilename))
}

func TestDefaultPathsUsesInvokrHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("INVOKR_HOME", home)

	paths := PathsFor(config.DefaultConfigDir())

	if paths.HomeDir != home {
		t.Fatalf("expected default paths under INVOKR_HOME %q, got %q", home, paths.HomeDir)
	}
	if paths.ToolboxDir != filepath.Join(home, DefaultToolboxDir) {
		t.Fatalf("expected toolbox path under INVOKR_HOME, got %q", paths.ToolboxDir)
	}
}

func TestRunQuestionnaireUsesDisplayedDefaults(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)

	in := strings.NewReader("\n\n\n\n\n")
	var out bytes.Buffer

	result, err := RunQuestionnaire(in, &out)
	if err != nil {
		t.Fatalf("RunQuestionnaire returned error: %v", err)
	}

	wantHome := filepath.Join(userHome, ".invokr")
	if result.HomeDir != wantHome {
		t.Fatalf("expected default home %q, got %q", wantHome, result.HomeDir)
	}
	if result.ProviderBaseURL != constants.DefaultProviderBaseURL {
		t.Fatalf("expected default base URL, got %q", result.ProviderBaseURL)
	}
	if !strings.Contains(out.String(), "Invokr setup: LLM provider base URL") {
		t.Fatalf("expected base URL prompt in questionnaire output, got %q", out.String())
	}
	if !strings.Contains(out.String(), "Invokr home path") {
		t.Fatalf("expected home prompt in questionnaire output, got %q", out.String())
	}
	if strings.Contains(strings.ToLower(out.String()), "llama") {
		t.Fatalf("setup must not mention llama, got %q", out.String())
	}
	if !strings.Contains(out.String(), "Enable built-in memory recall") {
		t.Fatalf("expected built-in memory prompt, got %q", out.String())
	}

	cfg := readConfig(t, result.ConfigPath)
	if cfg.Provider.BaseURL != constants.DefaultProviderBaseURL {
		t.Fatalf("expected config base URL %q, got %q", constants.DefaultProviderBaseURL, cfg.Provider.BaseURL)
	}
	if cfg.Memory.Dir != filepath.Join(wantHome) {
		t.Fatalf("expected memory dir under selected home, got %q", cfg.Memory.Dir)
	}
}

// Accepting the default from an isolated or scripted run must land in the
// INVOKR_HOME the process was launched with, not the operator's real
// ~/.invokr — that is what INVOKR_HOME is for.
func TestRunQuestionnaireHomeDefaultHonorsInvokrHomeEnv(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	wantHome := filepath.Join(t.TempDir(), "isolated-home")
	t.Setenv("INVOKR_HOME", wantHome)

	in := strings.NewReader("\n\n\n\n\n")
	var out bytes.Buffer

	result, err := RunQuestionnaire(in, &out)
	if err != nil {
		t.Fatalf("RunQuestionnaire returned error: %v", err)
	}

	if result.HomeDir != wantHome {
		t.Fatalf("expected default home to follow INVOKR_HOME (%q), got %q", wantHome, result.HomeDir)
	}
	if strings.Contains(out.String(), "~/.invokr") {
		t.Fatalf("expected the operator's real home not to appear as a default, got %q", out.String())
	}
}

func TestRunQuestionnaireUsesCustomAnswers(t *testing.T) {
	home := filepath.Join(t.TempDir(), "custom-invokr")
	in := strings.NewReader("http://llm.local:9090\n" + home + "\nno\ncatppuccin-mocha\nno\n")
	var out bytes.Buffer

	result, err := RunQuestionnaire(in, &out)
	if err != nil {
		t.Fatalf("RunQuestionnaire returned error: %v", err)
	}

	if result.HomeDir != home {
		t.Fatalf("expected custom home %q, got %q", home, result.HomeDir)
	}
	if result.ProviderBaseURL != "http://llm.local:9090" {
		t.Fatalf("expected custom base URL, got %q", result.ProviderBaseURL)
	}

	cfg := readConfig(t, result.ConfigPath)
	if cfg.Provider.BaseURL != "http://llm.local:9090" {
		t.Fatalf("expected config base URL, got %q", cfg.Provider.BaseURL)
	}
	if cfg.Memory.Dir != filepath.Join(home) {
		t.Fatalf("expected memory dir under selected home, got %q", cfg.Memory.Dir)
	}
}

func TestEnsureConfiguredUpdatesProviderBaseURL(t *testing.T) {
	home := t.TempDir()

	result, err := EnsureConfigured(home, Answers{ProviderBaseURL: "http://new.local:8080", MemoryEnabled: true, SyntaxTheme: "catppuccin-mocha"})
	if err != nil {
		t.Fatalf("EnsureConfigured returned error: %v", err)
	}

	cfg := readConfig(t, result.ConfigPath)
	if cfg.Provider.BaseURL != "http://new.local:8080" {
		t.Fatalf("expected configured base URL, got %q", cfg.Provider.BaseURL)
	}
	if !cfg.Memory.Zerfoo.Enabled {
		t.Fatal("expected built-in memory enabled in config")
	}
	if cfg.Memory.Dir != filepath.Join(home) {
		t.Fatalf("expected memory dir under configured home, got %q", cfg.Memory.Dir)
	}
}

func readConfig(t *testing.T, path string) config.AppConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The syntax theme is the one optional answer: blank means "match the
// terminal", which the config stores as an absent value rather than a name.
func TestQuestionnaireRecordsTheOptionalSyntaxTheme(t *testing.T) {
	for name, tc := range map[string]struct{ answer, want string }{
		"a name":       {"catppuccin-mocha", "catppuccin-mocha"},
		"blank":        {"", ""},
		"auto":         {"auto", ""},
		"auto, spaced": {"  Auto  ", ""},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			in := strings.NewReader("http://llm.local:9090\n" + home + "\nno\n" + tc.answer + "\nno\n")
			result, err := RunQuestionnaire(in, &bytes.Buffer{})
			if err != nil {
				t.Fatalf("RunQuestionnaire returned error: %v", err)
			}
			if result.SyntaxTheme != tc.want {
				t.Fatalf("SyntaxTheme = %q, want %q", result.SyntaxTheme, tc.want)
			}
			if cfg := readConfig(t, result.ConfigPath); cfg.UI.SyntaxTheme != tc.want {
				t.Fatalf("config syntax_theme = %q, want %q", cfg.UI.SyntaxTheme, tc.want)
			}
		})
	}
}

func TestModelOptionsListsProviderModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"model-a"},{"id":"model-b"}]}`)
	}))
	defer server.Close()

	options := (&modelProbe{}).options(server.URL, nil)
	if len(options) != 3 {
		t.Fatalf("expected two models plus the default option, got %d", len(options))
	}
	if options[0].Value != "model-a" || options[1].Value != "model-b" {
		t.Fatalf("unexpected model options: %+v", options)
	}
	if options[2].Value != keepDefaultModel {
		t.Fatalf("expected the default option last, got %+v", options[2])
	}
}

func TestModelProbeSaysWhyTheListIsEmpty(t *testing.T) {
	probe := &modelProbe{}
	if options := probe.options("http://127.0.0.1:1", nil); len(options) != 1 || options[0].Value != keepDefaultModel {
		t.Fatalf("expected only the default option, got %+v", options)
	}
	if status := probe.status("http://127.0.0.1:1"); !strings.Contains(status, "Can't reach the model server at 127.0.0.1:1") {
		t.Fatalf("unreachable status = %q", status)
	}
	if status := probe.status("localhost:8080"); !strings.Contains(status, "starting with http://") {
		t.Fatalf("partial URL status = %q", status)
	}
	server := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer server.Close()
	if status := probe.status(server.URL); !strings.Contains(status, "lists no models") {
		t.Fatalf("no-list-endpoint status = %q", status)
	}
}

func TestEnsureConfiguredAndEnsureAtShareHomeSetup(t *testing.T) {
	for name, ensure := range map[string]func(string) (Result, error){
		"EnsureAt": EnsureAt,
		"EnsureConfigured": func(home string) (Result, error) {
			return EnsureConfigured(home, Answers{ProviderBaseURL: DefaultProviderBaseURL})
		},
	} {
		home := filepath.Join(t.TempDir(), ".invokr")
		t.Setenv("INVOKR_HOME", home)

		result, err := ensure(home)
		if err != nil {
			t.Fatalf("%s returned error: %v", name, err)
		}
		if !result.HomeCreated || !result.ConfigCreated || !result.PersonaCreated {
			t.Fatalf("%s: expected home, config and persona created, got %+v", name, result)
		}
		for _, path := range []string{result.ConfigPath, result.PersonaPath} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("%s: expected %q to exist: %v", name, path, err)
			}
		}
	}
}

func TestEnsureFirstRunFallsBackWhenNotATerminal(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".invokr")
	t.Setenv("INVOKR_HOME", home)

	result, err := EnsureFirstRun(strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatalf("EnsureFirstRun returned error: %v", err)
	}
	if !result.HomeCreated || !result.ConfigCreated {
		t.Fatalf("expected home and config to be created, got %+v", result)
	}
}

func TestValidSyntaxTheme(t *testing.T) {
	for _, ok := range []string{"auto", "AUTO", "monokai", "catppuccin-mocha", " dracula "} {
		if err := validSyntaxTheme(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "monokia", "solarized"} {
		if err := validSyntaxTheme(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPreviewThemeFollowsTheAnswer(t *testing.T) {
	cases := map[string]string{"": "tokyonight-night", "au": "tokyonight-night", "Monokai": "monokai", "drac": "dracula", "zzz": ""}
	for value, want := range cases {
		if got, _ := previewTheme(value); got != want {
			t.Errorf("previewTheme(%q) = %q, want %q", value, got, want)
		}
	}
	if got := themeDescription("monokai"); !strings.Contains(got, "\x1b[") || !strings.Contains(got, "greet") {
		t.Errorf("preview is not highlighted code: %q", got)
	}
	if got := themeDescription("zzz"); !strings.Contains(got, "No theme matches yet") {
		t.Errorf("unmatched preview = %q", got)
	}
}
