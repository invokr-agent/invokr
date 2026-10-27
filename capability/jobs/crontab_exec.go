package jobs

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/invokr-ai/invokr/support/fsutil"
	"os"
	"os/exec"
	"strings"
)

// readCrontab returns the current user's crontab, or "" if none is set.
func readCrontab() (string, error) {
	out, err := exec.Command("crontab", "-l").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && strings.Contains(string(exitErr.Stderr), "no crontab for") {
			return "", nil
		}
		return "", fmt.Errorf("jobs: read crontab: %w", err)
	}
	return string(out), nil
}

func writeCrontab(content string) error {
	c := exec.Command("crontab", "-")
	c.Stdin = bytes.NewReader([]byte(content))
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("jobs: write crontab: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Install reads the crontab, removes any existing entry for job id, adds a
// fresh entry running on schedule via the current binary, and writes it back.
func Install(schedule, id string) (string, error) {
	if _, err := exec.LookPath("crontab"); err != nil {
		return "", fmt.Errorf("jobs: crontab binary not found in PATH: %w", err)
	}
	binaryPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("jobs: resolve binary path: %w", err)
	}
	warning := temporaryExecutableWarning(binaryPath)
	content, err := readCrontab()
	if err != nil {
		return warning, err
	}
	stripped := StripCrontabEntries(content, id)
	updated, err := AddCrontabEntry(stripped, schedule, binaryPath, id)
	if err != nil {
		return warning, err
	}
	return warning, writeCrontab(updated)
}

func temporaryExecutableWarning(binaryPath string) string {
	if _, ok := fsutil.RelInside(os.TempDir(), binaryPath); !ok {
		return ""
	}
	return fmt.Sprintf("WARNING: executable %s is temporary; install Invokr before scheduling persistent jobs", binaryPath)
}

func Strip(id string) error {
	return stripCrontab(func(content string) string {
		return StripCrontabEntries(content, id)
	})
}

func StripAll() error { return stripCrontab(StripAllInvokrEntries) }

func stripCrontab(strip func(string) string) error {
	if _, err := exec.LookPath("crontab"); err != nil {
		return fmt.Errorf("jobs: crontab binary not found in PATH: %w", err)
	}
	content, err := readCrontab()
	if err != nil {
		return err
	}
	updated := strip(content)
	if updated == content {
		return nil
	}
	return writeCrontab(updated)
}
