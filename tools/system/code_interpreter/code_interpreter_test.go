package code_interpreter

import (
	"context"
	"encoding/json"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/tools/system/workspace"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

type captureDispatcher struct {
	events []events.Event
}

func (d *captureDispatcher) Dispatch(_ context.Context, event events.Event) {
	d.events = append(d.events, event)
}

func TestProcessBackendRunsPythonAndCollectsArtifact(t *testing.T) {
	root := t.TempDir()
	inputPath := filepath.Join(root, "data.txt")
	if err := os.WriteFile(inputPath, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	dispatcher := &captureDispatcher{}
	service := NewService(config.CodeInterpreterConfig{Backend: constants.CodeInterpreterBackendProcess, SessionDir: filepath.Join(root, ".ci")}, workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root}), dispatcher)
	prepared, err := service.Prepare(Args{
		Code:       "import os\nfrom pathlib import Path\nPath(os.environ['INVOKR_CI_ARTIFACT_DIR'], 'out.txt').write_text(Path('data.txt').read_text())\nprint('done')",
		InputFiles: []string{"data.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := service.Run(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "done" || result.ExitCode != 0 || result.TimedOut {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Preview != "hello" {
		t.Fatalf("unexpected artifacts: %+v", result.Artifacts)
	}
	if len(dispatcher.events) != 1 || dispatcher.events[0].Audit == nil || dispatcher.events[0].Audit.Outcome != "completed" {
		t.Fatalf("missing completed audit: %+v", dispatcher.events)
	}
}

func TestPrepareTreatsUnsafeTokensAsAdvisoryAndRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	service := NewService(config.CodeInterpreterConfig{Backend: constants.CodeInterpreterBackendProcess, SessionDir: filepath.Join(root, ".ci")}, workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root}), nil)
	prepared, err := service.Prepare(Args{Code: "import subprocess"})
	if err != nil || !slices.Contains(prepared.advisories, "import subprocess") {
		t.Fatalf("preflight advisories = %#v, error = %v", prepared.advisories, err)
	}
	prepared, err = service.Prepare(Args{Code: `importlib.import_module("subprocess")`})
	if err != nil || !slices.Contains(prepared.advisories, `importlib.import_module("subprocess"`) {
		t.Fatalf("preflight advisories = %#v, error = %v", prepared.advisories, err)
	}
	if _, err := service.Prepare(Args{Code: "print(1)", InputFiles: []string{"../outside.txt"}}); err == nil {
		t.Fatal("expected outside path rejection")
	}
}

func TestNewServicePrefersConfiguredContainerEngineWhenAvailable(t *testing.T) {
	binDir := t.TempDir()
	engine := filepath.Join(binDir, "fake-container")
	if err := os.WriteFile(engine, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	service := NewService(config.CodeInterpreterConfig{ContainerEngine: "fake-container"}, workspace.Policy{}, nil)
	if service.cfg.Backend != constants.CodeInterpreterBackendContainer {
		t.Fatalf("backend = %q, want container", service.cfg.Backend)
	}
}

func TestContainerArgvIsSafeByDefault(t *testing.T) {
	root := t.TempDir()
	cfg := config.AppConfig{CodeInterpreter: config.CodeInterpreterConfig{
		Backend:         constants.CodeInterpreterBackendContainer,
		SessionDir:      filepath.Join(root, ".ci"),
		ContainerEngine: "podman",
	}}.WithDefaults().CodeInterpreter
	p := prepared{
		cfg:         cfg,
		sessionDir:  filepath.Join(root, ".ci", "s"),
		inputDir:    filepath.Join(root, ".ci", "s", "inputs"),
		artifactDir: filepath.Join(root, ".ci", "s", "artifacts"),
		roots:       []string{root},
	}
	argv, err := BuildContainerArgv(p)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, "\x00")
	for _, want := range []string{"podman", "--rm", "--network=none", ":/work/inputs:ro", ":/work/artifacts:rw", "PYTHONPATH=", root + ":" + root + ":ro", "-w\x00" + root} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv missing %q: %#v", want, argv)
		}
	}
	if slices.Contains(argv, "--privileged") || strings.Contains(joined, "/var/run/docker.sock") {
		t.Fatalf("unsafe container argv: %#v", argv)
	}
}

func TestTrimOutputDoesNotSplitMultiByteRune(t *testing.T) {
	// "café" is 5 bytes: c(1) a(1) f(1) é(2). Clamping at 4 bytes lands mid-é.
	text := "café"
	if got, want := len(text), 5; got != want {
		t.Fatalf("test fixture len(text) = %d, want %d", got, want)
	}
	got := trimOutput(text, 4)
	if !utf8.ValidString(got) {
		t.Fatalf("trimOutput(%q, 4) = %q, not valid UTF-8", text, got)
	}
	if want := "caf"; got != want {
		t.Fatalf("trimOutput(%q, 4) = %q, want %q", text, got, want)
	}
}

func TestTrimOutputKeepsShortTextUnchanged(t *testing.T) {
	if got, want := trimOutput("  hello  ", 100), "hello"; got != want {
		t.Fatalf("trimOutput = %q, want %q", got, want)
	}
}

func TestTrimOutputZeroOrNegativeMaxBytesDisablesTrim(t *testing.T) {
	text := "hello world"
	if got := trimOutput(text, 0); got != text {
		t.Fatalf("trimOutput(_, 0) = %q, want %q", got, text)
	}
	if got := trimOutput(text, -1); got != text {
		t.Fatalf("trimOutput(_, -1) = %q, want %q", got, text)
	}
}

// Both backends mount the workspace roots read-only as the interpreter's cwd,
// so the approval card must advertise read_workspace.
func TestMetadataTiersIncludeReadWorkspace(t *testing.T) {
	tiers := metadataTiers(config.CodeInterpreterConfig{Backend: constants.CodeInterpreterBackendProcess})
	if !slices.Contains(tiers, constants.CodeInterpreterTierReadWorkspace) {
		t.Fatalf("tiers do not advertise read_workspace: %v", tiers)
	}
	if !slices.Contains(tiers, constants.CodeInterpreterTierArtifactWrite) || !slices.Contains(tiers, constants.CodeInterpreterTierCodeExecution) {
		t.Fatalf("unexpected tiers: %v", tiers)
	}
}

// Hidden components of the workspace root itself must not disqualify input
// files inside it (issue #326): the hidden check runs on the display path,
// not the absolute path.
func TestResolveInputsAllowsFilesUnderHiddenRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".work", "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	file := filepath.Join(root, "data.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root})

	inputs, err := resolveInputs([]string{"data.txt"}, wsp)
	if err != nil {
		t.Fatalf("resolveInputs under hidden root: %v", err)
	}
	if len(inputs) != 1 || inputs[0].AbsPath != file {
		t.Fatalf("resolveInputs = %+v, want one input at %s", inputs, file)
	}

	hidden := filepath.Join(root, ".secret")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatalf("mkdir hidden: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "key.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write hidden file: %v", err)
	}
	if _, err := resolveInputs([]string{".secret/key.txt"}, wsp); err == nil {
		t.Fatalf("resolveInputs accepted hidden path inside workspace")
	}
}

// output_files is the one path by which this tool touches the workspace: the
// sandbox still never sees the workspace, the host copies named artifacts back
// afterwards.
func TestOutputFilesCopyArtifactsBackToWorkspace(t *testing.T) {
	root := t.TempDir()
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	svc := NewService(config.CodeInterpreterConfig{}, wsp, nil)

	p, err := svc.Prepare(Args{Code: "x = 1", OutputFiles: []string{"organization.json"}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(p.outputs) != 1 || p.outputs[0].Artifact != "organization.json" {
		t.Fatalf("outputs = %+v", p.outputs)
	}

	if err := os.MkdirAll(p.artifactDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.artifactDir, "organization.json"), []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	written, missing, err := copyOutputs(p)
	if err != nil {
		t.Fatalf("copyOutputs: %v", err)
	}
	if len(written) != 1 || len(missing) != 0 {
		t.Fatalf("written=%v missing=%v", written, missing)
	}
	got, err := os.ReadFile(filepath.Join(root, "organization.json"))
	if err != nil || string(got) != `{"ok":true}` {
		t.Fatalf("workspace file = %q, err %v", got, err)
	}
}

// A requested output the code never wrote is reported, not raised: the run
// itself succeeded and the model needs to see which name it missed.
func TestOutputFilesReportMissingArtifact(t *testing.T) {
	root := t.TempDir()
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	svc := NewService(config.CodeInterpreterConfig{}, wsp, nil)
	p, err := svc.Prepare(Args{Code: "x = 1", OutputFiles: []string{"missing.json"}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	written, missing, err := copyOutputs(p)
	if err != nil {
		t.Fatalf("copyOutputs: %v", err)
	}
	if len(written) != 0 || len(missing) != 1 || missing[0] != "missing.json" {
		t.Fatalf("written=%v missing=%v", written, missing)
	}
}

// An output path outside the workspace is refused at prepare time, before any
// code runs.
func TestOutputFilesRejectEscapingPath(t *testing.T) {
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: t.TempDir()})
	svc := NewService(config.CodeInterpreterConfig{}, wsp, nil)
	if _, err := svc.Prepare(Args{Code: "x = 1", OutputFiles: []string{"../escape.json"}}); err == nil {
		t.Fatal("an escaping output path must be refused before the run")
	}
}
