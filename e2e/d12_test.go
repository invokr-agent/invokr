//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestLiveD12_RememberThenRecallAcrossSessions drives the reported user
// journey end-to-end through the real agent loop: session one reads a
// workspace doc and commits a marked fact to long-term memory; a fresh exec
// run (new session, same home) must surface that fact again via the memory
// subsystem. D05 covers this at the manager level and C11 only checks memory
// tool selection — this case is the only one that ties store and cross-session
// retrieval together through live model behavior.
func TestLiveD12_RememberThenRecallAcrossSessions(t *testing.T) {
	baseURL := requireLive(t)
	home := newHome(t, baseURL)
	home.live = true
	const marker = "live-d12-gateway-owns-recall"

	stored := runExec(t, home, baseURL,
		"Read docs/architecture.md, then save one fact to long-term project memory stating which "+
			"module owns recall. The stored content must end with the literal string "+marker+".")
	if stored.Err != nil {
		t.Fatalf("[D12] store exec failed: exit=%d err=%v", stored.ExitCode, stored.Err)
	}
	if !ledgerSawTool(parseLedger(t, stored.Stdout), "memory") {
		t.Fatalf("[D12] model did not call the memory tool to store the fact")
	}
	var persisted bool
	rows := memoryRows(t, home)
	for _, row := range rows {
		persisted = persisted || (row.Status == "active" && strings.Contains(row.Content, marker))
	}
	if !persisted {
		t.Fatalf("[D12] memory tool ran but no active record contains marker %q; rows=%#v", marker, rows)
	}

	// Fresh exec run = fresh session; only the shared memory store can carry
	// the marker across, so echoing it proves recall rather than re-reading.
	recalled := runExec(t, home, baseURL,
		"Without reading any files: what fact tagged "+marker[:len("live-d12")]+"* did you store in "+
			"long-term memory earlier? Search your memory and repeat the fact with its exact marker.")
	if recalled.Err != nil {
		t.Fatalf("[D12] recall exec failed: exit=%d err=%v", recalled.ExitCode, recalled.Err)
	}
	ledger := parseLedger(t, recalled.Stdout)
	if !ledgerSawTool(ledger, "recall") && !ledgerSawTool(ledger, "memory") {
		t.Errorf("[D12] model consulted neither recall nor memory tool in the fresh session")
	}
	if ledgerSawTool(ledger, "read_file") {
		t.Errorf("[D12] model re-read files instead of relying on memory")
	}
	if !strings.Contains(recalled.Stdout, marker) {
		t.Fatalf("[D12] fresh session did not reproduce marker %q — stored memory was not recalled", marker)
	}
}
