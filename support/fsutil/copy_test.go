package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(src, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if err := CopyFile(src, dst, 0o600); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("dst contents = %q", got)
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("dst perm = %o, want 600", info.Mode().Perm())
	}
}

func TestCopyFileTruncatesExisting(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.WriteFile(dst, []byte("much longer old content"), 0o644); err != nil {
		t.Fatalf("write dst: %v", err)
	}

	if err := CopyFile(src, dst, 0o755); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("dst contents = %q, want %q", got, "new")
	}
}

func TestCopyFileMissingSource(t *testing.T) {
	dir := t.TempDir()
	err := CopyFile(filepath.Join(dir, "nope.txt"), filepath.Join(dir, "dst.txt"), 0o600)
	if err == nil {
		t.Fatal("expected error for missing source")
	}
}
