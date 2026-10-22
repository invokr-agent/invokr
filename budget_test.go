package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	metrics "github.com/antonikliment/go-code-metrics/analysis"
)

// Per-area implementation LOC budgets.
//
// The goclocbudget linter enforces one repository-wide ceiling
// (.golangci.yml). That number cannot say *where* growth happened, so a TUI
// polish PR and a correctness fix compete for the same headroom — see
// docs/private/reviews/2026-08-25-loc-vs-peer-harnesses.md. These per-area budgets are
// the local ceilings underneath it: the repo cap is still the hard aggregate
// limit, and no single area may grow into everyone else's room.
//
// The budgets deliberately sum above the repo cap. Both constraints bind: an
// area can be under its own budget and still be refused by the repo cap.
//
// Raising an entry is a normal reviewable change. Raising several at once, or
// raising the repo cap to fit, is the signal to stop and reconsider the shape
// of the change instead.
//
// Set INVOKR_BUDGET_REPORT=1 to print the current table instead of asserting,
// which is how the numbers below were calibrated.

// area is one budgeted slice of the tree. Prefixes are matched longest-first,
// so "cmd/agentui" wins over "cmd" without depending on declaration order.
type area struct {
	prefix string
	budget int
}

var budgets = []area{
	// Surfaces. The TUI is the thinnest subsystem against comparable
	// harnesses (3.8k here against crush's 27k), so it gets the most room:
	// the cap should not be what stops the TUI catching up.
	{"cmd/agentui", 6000},
	{"cmd", 2500},
	// 2026-10-22 measured count: the installer downloads, verifies and swaps the
	// release binary in Go instead of running install.sh.
	{"internal", 150},

	// Agent loop and tool plumbing — the core of the core.
	// 2026-09-17 +95: recoverable compaction archives, paired pin budget,
	// post-compaction headroom and authoritative workspace snapshot. Raised 2900→2960
	// for the reasoning-effort setting (gateway field, provider caps,
	// session accessors) landing on top of the #464 harness spike. Raised
	// 2960->2990 for truncated-tool-call recovery in the gateway. Raised
	// 3075->3100 for the parse give-up summary completion; ->3120 for the
	// repeat give-up summary path and the empty-final reason; ->3140 for the
	// tail elision pass and the router-aware context-window probe.
	{"agent", 3775}, // 2026-09-27 +10: log a turn's full reasoning text on the events bus (#577). 2026-09-16 -59: standing reminder removed, benched neutral/negative (#566). 2026-09-16 +16: refresh the system message on every continued turn so the todo block is live (#566). 2026-09-15 +1: todo block wired into the prompt builder (#566). 2026-09-15 +3: notice per reminder injection (#566). 2026-09-15 +36: event-gated standing reminder (edit_file failure or unchecked edits) replacing the every-request one that looped b47 (#566). 2026-09-12 +24: standing end-of-context reminder on every continued turn, plus the gateway callback that attributes native schema chars to the recorded prompt (#566). 2026-09-12 -11 (3659→3648): revert #558's compaction carry and 4→16 KB pin cap raise, keeping its 32 KB total pin budget (#559). 2026-09-11 +14: newest read per path carried through compaction, read pin cap 4→16 KB under a 32 KB budget (#553). 2026-09-10 +2: distinct provider_error stop reason for a run the provider ended (#539). 2026-09-10 +4: stop spending recoveries on a parse rejection that repeats byte for byte (#539). 2026-09-10 +27: keep unparseable tool-call arguments out of the transcript and hint to split the edit (#539).
	// 2026-09-10 +9: budget-derived spill page hint and read_file cap bounded by the gate budget (#536). 2026-09-10 +19: carry the dropped-reads notice through repeated compactions (#532). 2026-09-10 +5: prefetch coverage gate so a chunked summary cannot pass for a whole compaction (#529). 2026-09-09 +10: no-think summarizer retry (#515). 2026-09-09 +7: dropped-reads compaction notice (#516). 2026-09-09 +16: consecutive parse-failure extension gate (#490). 2026-09-07 +95: per-turn tool result aging (#480). 2026-09-07 +10: per-tool schema size debug log (#480). 2026-09-07 +31: zero-tool give-up nudge (#442). 2026-09-07 +50: step-limit stop reason and incomplete-work notice, plus the peer's context budget/retention pass 2026-09-09 +3: truncated-stream retry and prefetch window gate (#499). 2026-09-09 +1: review nits for the truncated-stream retry. 2026-09-09 +45: degenerate-reply nudge after the reasoning-overflow retry and the retry ledger counter (#503). 2026-09-09 +8: bounded summarizer completion (#506). 2026-09-09 +40: reserve summary headroom when the pin meets the engine window; warn below 64k (#507, #495). 2026-09-09 +12: fold review - reserve headroom on every discovered window, pinned-window-below-minimum warning (#507, #495). 2026-09-09 +22: opt-in guard annotating a verification claim the ledger cannot support (#493). 2026-09-09 +9: chunked summary prefetch (#513).
	{"tools/core", 1344},    // 2026-10-22 -30: the unwrap helpers fold into one; exact measured count. 2026-10-22 -2: the field-level unwrap narrows to the field the decode error names, so a genuine string field keeps its text; exact measured count. 2026-10-22 +55: one argument field a server stringifies unwraps in the shared decoder (whole-object and field-level shapes), plus the retry helper that keeps the decoder under the complexity gates; exact measured count. 2026-10-22 +12: tool arguments a server double-encodes unwrap in the shared decoder; exact measured count. 2026-10-11 +58: pin Git binds and close inherited descriptors before payload exec (#577 F1).
	{"tools/system", 10843}, // 2026-10-19 +6: reroute folds echo-separated cat &&/; chains into one batched read (net of a trim pass). 2026-10-19 +51: shell approval details show the script a line runs (skips one this line writes; host route too). 2026-10-19 +175: shell lines a native tool covers reroute to it (read, file_search, git_read, edit). 2026-10-19 +144: git_write push (host git, approval-gated) + shell push redirect. 2026-10-19 +82: approval-gated reads outside the workspace (read, file_search dir). 2026-10-18 +63: toolbox hub list/install for `toolbox install` and the /tools Install tab. 2026-09-21 +19: shell returns plain text (#577). On dev 690e45c: multi-file read_file and first-line block anchor (#577 bench). 2026-09-20 +30 on the #597 base: edit_file block-anchor rung (#577 bench arm)
	{"tools", 600},          // tools/, tools/integrations

	// Capabilities. Memory is budgeted apart from the rest because it is by
	// far the largest and has its own extraction discussion on file
	// (docs/private/plans/capability-bundles.md).
	{"capability/memory", 4800},
	{"capability", 4700},

	// Platform, config, clients, surfaces, leaf helpers.
	{"platform", 2500},
	{"config", 1376}, // 2026-10-19 +14: workspace.reads. 2026-10-16: merged per-model options and JSON profiles (#709, #708).
	{"clients", 800},
	{"surface", 1600},
	{"support", 500},
	{"constants", 300},
}

// excludedDirs mirrors .golangci.yml's goclocbudget exclude-dirs so the two
// checks measure the same tree. Keep them in step.
var excludedDirs = []string{
	"vendor", ".git", "node_modules", "app/dist",
	"support/testutil", "cmd/e2ereport", "bench", ".claude",
}

func TestPerAreaLOCBudgets(t *testing.T) {
	byArea, unassigned := measure(t)

	if len(unassigned) > 0 {
		sort.Strings(unassigned)
		t.Errorf("no budget covers these paths — add an area to budgets: %s",
			strings.Join(unassigned, ", "))
	}

	if os.Getenv("INVOKR_BUDGET_REPORT") != "" {
		report(t, byArea)
		return
	}

	for _, a := range budgets {
		if got := byArea[a.prefix]; got > a.budget {
			t.Errorf("%s: %d implementation lines exceeds its budget of %d (over by %d)",
				a.prefix, got, a.budget, got-a.budget)
		}
	}
}

// measure aggregates implementation code lines per budgeted area, and reports
// any file that no area claims.
func measure(t *testing.T) (map[string]int, []string) {
	t.Helper()
	tree, err := metrics.Analyze(metrics.Options{
		Root:             ".",
		IncludeTests:     false,
		ExcludeGenerated: true,
		ExcludeDirs:      excludedDirs,
	})
	if err != nil {
		t.Fatalf("analyze repository: %v", err)
	}
	byArea := map[string]int{}
	var unassigned []string
	for _, f := range metrics.Files(tree) {
		rel := filepath.ToSlash(f.Path)
		// Files at the repository root (main.go) belong to no area and are
		// covered by the repo-wide cap alone.
		if !strings.Contains(rel, "/") {
			continue
		}
		if prefix, ok := areaFor(rel); ok {
			byArea[prefix] += f.Code
			continue
		}
		unassigned = append(unassigned, rel)
	}
	return byArea, unassigned
}

// areaFor returns the longest budgeted prefix containing rel.
func areaFor(rel string) (string, bool) {
	best := ""
	for _, a := range budgets {
		if rel == a.prefix || strings.HasPrefix(rel, a.prefix+"/") {
			if len(a.prefix) > len(best) {
				best = a.prefix
			}
		}
	}
	return best, best != ""
}

func report(t *testing.T, byArea map[string]int) {
	t.Helper()
	total, budgeted := 0, 0
	for _, a := range budgets {
		got := byArea[a.prefix]
		total += got
		budgeted += a.budget
		t.Logf("%-20s %6d / %6d  (%+d)", a.prefix, got, a.budget, a.budget-got)
	}
	t.Logf("%-20s %6d / %6d", "TOTAL", total, budgeted)
	fmt.Fprintf(os.Stderr, "budgeted areas total: %d implementation lines\n", total)
}
