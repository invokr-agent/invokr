package logging

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
)

func TestBufferKeepsRecentCompleteLines(t *testing.T) {
	buf := NewBuffer(2)

	_, _ = buf.Write([]byte("one\n"))
	_, _ = buf.Write([]byte("two\nthree"))
	_, _ = buf.Write([]byte("\n"))

	got := buf.Lines()
	want := []string{"two", "three"}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
}

func TestNewWithSinkWritesFileAndBuffer(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "nested", "invokr.log")
	buf := NewBuffer(10)

	logger, err := NewWithSink(config.LoggingConfig{
		Level:  "info",
		Format: "console",
		Output: logPath,
	}, buf)
	if err != nil {
		t.Fatalf("NewWithSink() error = %v", err)
	}

	logger.Info("agent log line")
	if err := logger.Sync(); err != nil {
		t.Fatalf("logger.Sync() error = %v", err)
	}

	fileContent := readTestFile(t, logPath)
	if !strings.Contains(fileContent, "agent log line") {
		t.Fatalf("log file missing line: %q", fileContent)
	}

	if got := strings.Join(buf.Lines(), "\n"); !strings.Contains(got, "agent log line") {
		t.Fatalf("buffer missing line: %q", got)
	}
}

func TestNewWithSinkTruncatesDefaultLogOnRestart(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	logPath := config.DefaultLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("stale run\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	logger, err := NewWithSink(config.LoggingConfig{Level: "info", Format: "console", Output: logPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("current run")
	if err := logger.Sync(); err != nil {
		t.Fatal(err)
	}
	content := readTestFile(t, logPath)
	if strings.Contains(content, "stale run") || !strings.Contains(content, "current run") {
		t.Fatalf("default log was not restarted cleanly: %q", content)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
