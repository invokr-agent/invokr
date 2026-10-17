//go:build e2e

package e2e

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/support/testutil/clitest"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

func TestR09_CorruptSQLiteFailsIntegrityCheck(t *testing.T) {
	seedProvider := providertest.NewScripted(t,
		managedToolStep("r09-a", "memory", map[string]any{"action": "save", "content": "r09-alpha"}),
		managedToolStep("r09-b", "memory", map[string]any{"action": "save", "content": "r09-beta"}),
		providertest.ReplyStep("seeded"),
	)
	defer seedProvider.Close()
	home := newHome(t, seedProvider.URL())
	seed := runExec(t, home, seedProvider.URL(), "seed memory")
	if seed.Err != nil {
		t.Fatalf("seed exec: %v %s", seed.Err, seed.Stderr)
	}
	dbPath := filepath.Join(home.state.MemoryDir, "memory.db")
	corruptSQLiteHeader(t, dbPath)
	if ok, detail := sqliteIntegrity(t, dbPath); ok {
		t.Fatalf("corruption did not damage sqlite: %s", detail)
	}

	provider := providertest.NewScripted(t)
	defer provider.Close()
	result := clitest.Run(t, clitest.Options{
		State: home.state,
		CommandArgs: []string{
			"--base-url=" + provider.URL(), "exec", "--json", "list memory",
		},
		Timeout: 15 * time.Second,
	})
	if result.Err == nil || result.ExitCode == 0 || result.TimedOut {
		t.Fatalf("corrupt db result: exit=%d timeout=%v err=%v", result.ExitCode, result.TimedOut, result.Err)
	}
	if !strings.Contains(result.Stderr, "failed integrity check") || !strings.Contains(result.Stderr, dbPath) {
		t.Fatalf("stderr does not name integrity failure and path: %s", result.Stderr)
	}
	if len(provider.Snapshot().CompletionRequests) != 0 {
		t.Fatalf("provider called after corrupt-store startup failure")
	}
	provider.AssertClean(t)
}

func corruptSQLiteHeader(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 164 {
		t.Fatalf("sqlite file too small: %d", len(data))
	}
	for i := 100; i < 164; i++ {
		data[i] ^= 0xff
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func sqliteIntegrity(t *testing.T, path string) (bool, string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return false, err.Error()
	}
	defer db.Close()
	var detail string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&detail); err != nil {
		return false, err.Error()
	}
	return detail == "ok", detail
}
