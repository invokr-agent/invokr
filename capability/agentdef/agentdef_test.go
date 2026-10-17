package agentdef

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustParseFile(t *testing.T, path string, data []byte) Definition {
	t.Helper()
	def, err := ParseFile(path, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return def
}

func mustParseError(t *testing.T, path string, data []byte) string {
	t.Helper()
	_, err := ParseFile(path, data)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	return err.Error()
}

const validFile = `---
name: code-reviewer
description: Focused code-review subagent.
model: gpt-4o-mini
max_turns: 6
tools:
  - read_file
  - list_directory
---
You are a careful code reviewer.`

func TestParseFile_ValidFull(t *testing.T) {
	def := mustParseFile(t, "test.md", []byte(validFile))
	if def.Name != "code-reviewer" {
		t.Errorf("name: got %q want %q", def.Name, "code-reviewer")
	}
	if def.Description != "Focused code-review subagent." {
		t.Errorf("description mismatch")
	}
	if def.Model != "gpt-4o-mini" {
		t.Errorf("model: got %q", def.Model)
	}
	if def.MaxTurns != 6 {
		t.Errorf("max_turns: got %d want 6", def.MaxTurns)
	}
	if !def.ToolsExplicit {
		t.Errorf("ToolsExplicit should be true")
	}
	if len(def.Tools) != 2 {
		t.Errorf("tools len: got %d want 2", len(def.Tools))
	}
	if def.Instructions != "You are a careful code reviewer." {
		t.Errorf("instructions: got %q", def.Instructions)
	}
}

func TestParseFile_OmittedTools(t *testing.T) {
	data := []byte("---\nname: helper\ndescription: A helper.\n---\nDo things.\n")
	def := mustParseFile(t, "test.md", data)
	if def.ToolsExplicit {
		t.Error("ToolsExplicit should be false when tools key absent")
	}
	if def.Tools != nil {
		t.Errorf("Tools should be nil, got %v", def.Tools)
	}
}

func TestParseFile_EmptyToolsList(t *testing.T) {
	data := []byte("---\nname: summarizer\ndescription: Summarizer.\ntools: []\n---\nSummarize only.\n")
	def := mustParseFile(t, "test.md", data)
	if !def.ToolsExplicit {
		t.Error("ToolsExplicit should be true when tools key present")
	}
	if len(def.Tools) != 0 {
		t.Errorf("Tools should be empty slice, got %v", def.Tools)
	}
}

func TestParseFile_MissingName(t *testing.T) {
	data := []byte("---\ndescription: No name.\n---\nSome body.\n")
	msg := mustParseError(t, "test.md", data)
	if !strings.Contains(msg, "name is required") {
		t.Errorf("error %q should contain 'name is required'", msg)
	}
}

func TestParseFile_InvalidNameChars(t *testing.T) {
	data := []byte("---\nname: My Agent\ndescription: Bad name.\n---\nBody.\n")
	msg := mustParseError(t, "test.md", data)
	if !strings.Contains(msg, "[a-z][a-z0-9-]*") {
		t.Errorf("error %q should contain regex pattern", msg)
	}
}

func TestParseFile_MissingDescription(t *testing.T) {
	data := []byte("---\nname: helper\n---\nBody.\n")
	msg := mustParseError(t, "test.md", data)
	if !strings.Contains(msg, "description is required") {
		t.Errorf("error %q should contain 'description is required'", msg)
	}
}

func TestParseFile_BlankModel(t *testing.T) {
	data := []byte("---\nname: helper\ndescription: A helper.\nmodel: \"   \"\n---\nBody.\n")
	msg := mustParseError(t, "test.md", data)
	if !strings.Contains(msg, "model must not be blank") {
		t.Errorf("error %q should mention blank model", msg)
	}
}

func TestParseFile_NegativeMaxTurns(t *testing.T) {
	data := []byte("---\nname: helper\ndescription: A helper.\nmax_turns: -1\n---\nBody.\n")
	msg := mustParseError(t, "test.md", data)
	if !strings.Contains(msg, "max_turns must be") {
		t.Errorf("error %q should mention max_turns", msg)
	}
}

func TestParseFile_EmptyBody(t *testing.T) {
	data := []byte("---\nname: helper\ndescription: A helper.\n---\n   \n")
	msg := mustParseError(t, "test.md", data)
	if !strings.Contains(msg, "instructions body is required") {
		t.Errorf("error %q should mention instructions body", msg)
	}
}

func TestParseFile_NoFrontmatter(t *testing.T) {
	data := []byte("Just plain markdown.\n")
	msg := mustParseError(t, "test.md", data)
	if !strings.Contains(msg, "name is required") {
		t.Errorf("error %q should contain 'name is required'", msg)
	}
}

func TestParseFile_UnterminatedFrontmatter(t *testing.T) {
	data := []byte("---\nname: x\n")
	msg := mustParseError(t, "test.md", data)
	if !strings.Contains(msg, "unterminated front matter") {
		t.Errorf("error %q should mention unterminated front matter", msg)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func TestNewRegistryFromDir_HappyPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "alpha.md", "---\nname: alpha\ndescription: Alpha.\n---\nDo alpha.\n")
	writeFile(t, dir, "beta.md", "---\nname: beta\ndescription: Beta.\n---\nDo beta.\n")

	reg, err := NewRegistryFromDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	names := reg.Names()
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %v", names)
	}
	if names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("unexpected names: %v", names)
	}
	d, ok := reg.Get("alpha")
	if !ok || d.Name != "alpha" {
		t.Errorf("Get alpha failed: %v %v", ok, d)
	}
}

func TestNewRegistryFromDir_NameCollision(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.md", "---\nname: foo\ndescription: First.\n---\nFirst.\n")
	writeFile(t, dir, "b.md", "---\nname: foo\ndescription: Second.\n---\nSecond.\n")

	_, err := NewRegistryFromDir(dir)
	if err == nil {
		t.Fatal("expected error for name collision, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "a.md") || !strings.Contains(msg, "b.md") {
		t.Errorf("collision error should name both source files, got: %s", msg)
	}
}

func TestNewRegistryFromDir_MissingDir(t *testing.T) {
	reg, err := NewRegistryFromDir("/nonexistent/path/to/agents")
	if err != nil {
		t.Fatalf("expected nil error for missing dir, got: %v", err)
	}
	if len(reg.Names()) != 0 {
		t.Errorf("expected empty registry, got %v", reg.Names())
	}
}

func TestNewRegistryFromDir_BadFilePlusGood(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "good.md", "---\nname: good\ndescription: Good.\n---\nGood.\n")
	writeFile(t, dir, "bad.md", "---\nname: BAD NAME\ndescription: Bad.\n---\nBad.\n")

	_, err := NewRegistryFromDir(dir)
	if err == nil {
		t.Fatal("expected error for bad file, got nil")
	}
}
