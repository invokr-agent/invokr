package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicWritesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := WriteFileAtomic(path, []byte("hello"), 0); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q, want %q", got, "hello")
	}
}

func TestWriteFileAtomicOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed WriteFile: %v", err)
	}
	if err := WriteFileAtomic(path, []byte("new"), 0); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("content = %q, want %q", got, "new")
	}
}

func TestWriteFileAtomicAppliesPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := WriteFileAtomic(path, []byte("data"), 0o640); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("perm = %v, want %v", info.Mode().Perm(), os.FileMode(0o640))
	}
}

func TestWriteFileAtomicNoTempFileLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := WriteFileAtomic(path, []byte("data"), 0); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "out.txt" {
		t.Fatalf("dir entries = %v, want only out.txt", entries)
	}
}

func TestWriteFileAtomicFailsForMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-subdir", "out.txt")
	if err := WriteFileAtomic(path, []byte("data"), 0); err == nil {
		t.Fatalf("expected error for missing directory")
	}
}
