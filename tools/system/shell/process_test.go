package shell

import (
	"encoding/json"
	"fmt"
	"github.com/invokr-agent/invokr/constants"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessStorePrunesExpiredAndExcessFinishedRecords(t *testing.T) {
	now := time.Now().UTC()
	store := &processStore{records: map[string]*processRecord{}, now: func() time.Time { return now }}
	for i := 0; i < maxRetainedProcesses+5; i++ {
		id := fmt.Sprintf("done-%03d", i)
		store.records[id] = &processRecord{ID: id, Done: true, FinishedAt: now.Add(time.Duration(i) * time.Second)}
	}
	store.records["active"] = &processRecord{ID: "active"}
	store.records["expired"] = &processRecord{ID: "expired", Done: true, FinishedAt: now.Add(-processRetention - time.Second)}

	_ = store.list("")
	if len(store.records) != maxRetainedProcesses+1 {
		t.Fatalf("retained records = %d, want %d finished plus active", len(store.records), maxRetainedProcesses+1)
	}
	if store.records["active"] == nil || store.records["expired"] != nil || store.records["done-000"] != nil {
		t.Fatalf("unexpected retained records: active=%v expired=%v oldest=%v", store.records["active"] != nil, store.records["expired"] != nil, store.records["done-000"] != nil)
	}
}

// TestReadProcessLogClampsMaxBytesToCap covers the fix that caps a
// model-supplied max_bytes at constants.ShellMaxOutputBytes instead of
// letting it request an arbitrarily large read into memory.
func TestReadProcessLogClampsMaxBytesToCap(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "big.log")
	big := strings.Repeat("x", constants.ShellMaxOutputBytes*2)
	if err := os.WriteFile(logPath, []byte(big), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	id := "clamp-test"
	processes.mu.Lock()
	processes.records[id] = &processRecord{ID: id, Done: true, LogPath: logPath}
	processes.mu.Unlock()
	t.Cleanup(func() {
		processes.mu.Lock()
		delete(processes.records, id)
		processes.mu.Unlock()
	})

	out, err := readProcessLog(processArgs{ProcessID: id, MaxBytes: constants.ShellMaxOutputBytes * 10})
	if err != nil {
		t.Fatalf("readProcessLog() error = %v", err)
	}
	var result processResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(result.Log) > constants.ShellMaxOutputBytes {
		t.Fatalf("log length = %d, want <= %d", len(result.Log), constants.ShellMaxOutputBytes)
	}
}

func TestPruneProcessLogsBoundsFilesAndRemovesExpired(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	for i := 0; i < maxRetainedProcesses+5; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%03d.log", i))
		if err := os.WriteFile(path, []byte("log"), 0600); err != nil {
			t.Fatal(err)
		}
		stamp := now.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	expired := filepath.Join(dir, "expired.log")
	if err := os.WriteFile(expired, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-processRetention - time.Second)
	if err := os.Chtimes(expired, old, old); err != nil {
		t.Fatal(err)
	}

	pruneProcessLogs(dir, now.Add(time.Duration(maxRetainedProcesses+5)*time.Second))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != maxRetainedProcesses-1 {
		t.Fatalf("retained logs = %d, want %d before adding the next log", len(entries), maxRetainedProcesses-1)
	}
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("expired log still exists: %v", err)
	}
}
