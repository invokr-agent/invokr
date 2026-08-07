//go:build e2e

package e2e

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/support/testutil/clitest"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// Case S01 — SWE task completion. The C/D/R/A tiers ask "did the agent route
// correctly"; this one asks the SWE-bench question instead: given a repository
// with a real failing test, does the agent produce a patch that makes the
// repository's own suite pass.
//
// Two tiers, per ADR 0004:
//
//   - TestS01 scripts the fix through the fake provider. It is bit-deterministic
//     and gates `make e2e`: it proves the fixture, the oracles, and the
//     prompt-in/patch-out path work, without a model.
//   - TestLiveS01 drives a real model (INVOKR_E2E_LIVE=1). Its score is a tracked
//     metric, not a gate — an unsolved task is reported ADVISORY/KNOWN, never as
//     a red build. Only harness breakage fails it.
//
// The three oracles are SWE-bench's, and all three are machine-checkable state
// (ADR 0004): a patch exists, the broken test now passes (FAIL_TO_PASS), and the
// tests that already passed still pass (PASS_TO_PASS). PASS_TO_PASS plus the
// tests/ tamper check are what stop "fix the bug by deleting the test".

// defaultSWETimeout bounds a single live SWE run. Real models read the repo,
// patch, and re-run tests, which is many multiples of the 120s ceiling
// execTimeout gives the short live cases.
const defaultSWETimeout = 10 * time.Minute

// sweProblemStatement is the SWE-bench "problem statement" analogue: the issue
// text, with no hint about which file or line is at fault.
const sweProblemStatement = `The textstats package miscounts words that differ only in case.

Reproduction:

    >>> from textstats.frequency import word_frequencies
    >>> word_frequencies("The the THE end")
    {'The': 1, 'the': 1, 'THE': 1, 'end': 1}

Expected {'the': 3, 'end': 1} — word_frequencies documents case-insensitive
counting. Fix the implementation so the repository's own test suite passes.
Do not modify anything under tests/.`

// FAIL_TO_PASS and PASS_TO_PASS targets, as unittest dotted paths.
const sweFailToPass = "tests.test_frequency.WordFrequenciesTest.test_counts_are_case_insensitive"

var swePassToPass = []string{
	"tests.test_frequency.WordFrequenciesTest.test_counts_repeated_words",
	"tests.test_frequency.WordFrequenciesTest.test_empty_text",
}

// sweFixtureFiles is the repository under test at its base commit: one buggy
// module, one suite holding both oracle classes, and a .gitignore so a model
// that runs the suite itself doesn't dirty the diff with __pycache__.
var sweFixtureFiles = map[string]string{
	".gitignore": "__pycache__/\n*.pyc\n",
	"textstats/__init__.py": `"""Small text statistics helpers."""
`,
	"textstats/frequency.py": `"""Word frequency helpers."""


def word_frequencies(text):
    """Return a mapping of word to occurrence count.

    Counting is case-insensitive: "The" and "the" are the same word.
    """
    counts = {}
    for word in text.split():
        counts[word] = counts.get(word, 0) + 1
    return counts
`,
	"tests/__init__.py": "",
	"tests/test_frequency.py": `import unittest

from textstats.frequency import word_frequencies


class WordFrequenciesTest(unittest.TestCase):
    def test_counts_repeated_words(self):
        self.assertEqual(word_frequencies("go go stop"), {"go": 2, "stop": 1})

    def test_empty_text(self):
        self.assertEqual(word_frequencies(""), {})

    def test_counts_are_case_insensitive(self):
        self.assertEqual(
            word_frequencies("The the THE end"), {"the": 3, "end": 1}
        )


if __name__ == "__main__":
    unittest.main()
`,
}

// sweScriptedPatch is the fake tier's stand-in for a model-authored patch: the
// minimal edit that satisfies all three oracles.
const sweScriptedPatch = `*** Begin Patch
*** Update File: textstats/frequency.py
@@
-    for word in text.split():
-        counts[word] = counts.get(word, 0) + 1
+    for word in text.split():
+        key = word.lower()
+        counts[key] = counts.get(key, 0) + 1
*** End Patch`

func TestS01_ScriptedPatchSatisfiesSWEOracles(t *testing.T) {
	provider := providertest.NewScripted(t,
		loadApplyPatch("s01-load"),
		providertest.NativeToolCallStep("s01-patch", "apply_patch", map[string]any{"patch": sweScriptedPatch}),
		providertest.ReplyStep("word_frequencies now lowercases each word before counting"),
	)
	defer provider.Close()
	home := newSWEHome(t, provider.URL())

	result := runSWEExec(t, home, provider.URL(), sweProblemStatement, 0)
	if result.Err != nil {
		t.Fatalf("[S01] exec failed: exit=%d err=%v\nstderr: %s", result.ExitCode, result.Err, result.Stderr)
	}
	if !ledgerSawTool(parseLedger(t, result.Stdout), "apply_patch") {
		t.Fatalf("[S01] scripted patch never reached the workspace:\n%s", result.Stdout)
	}
	if misses := sweOracles(t, home); len(misses) > 0 {
		t.Fatalf("[S01] scripted patch failed the SWE oracles:\n%s", strings.Join(misses, "\n"))
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

// TestS02 is the oracles' own negative control. A scoring harness that cannot
// fail is worth nothing, and both ways of failing are silent by construction:
// an agent that changes nothing still leaves a green PASS_TO_PASS, and an agent
// that edits the suite makes every test pass by definition.
func TestS02_SWEOraclesRejectNonFixes(t *testing.T) {
	home := newSWEHome(t, "http://127.0.0.1:1")

	misses := strings.Join(sweOracles(t, home), "\n")
	for _, want := range []string{"PATCH:", "FAIL_TO_PASS:"} {
		if !strings.Contains(misses, want) {
			t.Fatalf("[S02] untouched repository reported no %s miss:\n%s", want, misses)
		}
	}

	// Now the tamper case: satisfy the failing test by rewriting it.
	mustWrite(t, filepath.Join(home.workspace, "tests", "test_frequency.py"),
		strings.ReplaceAll(sweFixtureFiles["tests/test_frequency.py"], `{"the": 3, "end": 1}`, `word_frequencies("The the THE end")`))
	misses = strings.Join(sweOracles(t, home), "\n")
	if !strings.Contains(misses, "TESTS-MODIFIED:") {
		t.Fatalf("[S02] rewritten suite passed the oracles:\n%s", misses)
	}
	sweGit(t, home, "checkout", "--", "tests")

	// The added-file bypass: nothing tracked changes, so a diff-based tamper
	// check sees a clean suite while a new conftest.py neuters it. The oracle
	// reads `git status --porcelain`, which lists untracked paths too, so this
	// is caught — assert it, because a switch to `git diff --name-only` would
	// silently reopen the hole.
	mustWrite(t, filepath.Join(home.workspace, "tests", "conftest.py"), "# neutered\n")
	misses = strings.Join(sweOracles(t, home), "\n")
	if !strings.Contains(misses, "TESTS-MODIFIED:") || !strings.Contains(misses, "tests/conftest.py") {
		t.Fatalf("[S02] a file added under tests/ passed the oracles:\n%s", misses)
	}
}

func TestLiveS01_WordFrequencyBug(t *testing.T) {
	baseURL := requireLive(t)
	// Resolve the model before laying down the fixture: skipping is cheaper
	// than a git checkout and a baseline unittest run nothing will score.
	model := sweModel(t)
	home := newSWEHome(t, baseURL)
	home.live = true
	rewriteConfig(t, &home, func(app *config.AppConfig) { app.Provider.Model = model })

	result := runSWEExec(t, home, baseURL, sweProblemStatement, sweTimeout(t))
	// An exec that never completed is harness breakage, not a low score: fail
	// it rather than logging a miss the model never had a chance to avoid.
	if result.Err != nil {
		t.Fatalf("[S01] exec failed: exit=%d timedOut=%v err=%v\nstderr: %s",
			result.ExitCode, result.TimedOut, result.Err, result.Stderr)
	}
	misses := sweOracles(t, home)
	if len(misses) == 0 {
		t.Logf("S01 PASS: %s solved the task; patch applies, FAIL_TO_PASS and PASS_TO_PASS both green", model)
		return
	}
	for _, miss := range misses {
		t.Logf("ADVISORY[S01] KNOWN: %s (model %s)", miss, model)
	}
	t.Skipf("KNOWN: %s did not satisfy every SWE oracle; the score is a tracked metric, not a gate", model)
}

// newSWEHome lays the fixture repository over a standard e2e home and records
// its base commit, then asserts the starting state the oracles assume: the
// FAIL_TO_PASS test fails and the PASS_TO_PASS tests pass. Without that check a
// broken fixture would report every run as a solved task.
func newSWEHome(t *testing.T, providerURL string) testHome {
	t.Helper()
	requireSWETooling(t)
	home := newHome(t, providerURL)
	for path, body := range sweFixtureFiles {
		mustWrite(t, filepath.Join(home.workspace, path), body)
	}
	sweGit(t, home, "init", "-q")
	sweGit(t, home, "add", "-A")
	sweGit(t, home, "commit", "-q", "-m", "textstats fixture at base commit")

	if code, out := sweUnittest(t, home, sweFailToPass); code == 0 {
		t.Fatalf("baseline: FAIL_TO_PASS target already passes, the fixture bug is gone:\n%s", out)
	}
	if code, out := sweUnittest(t, home, swePassToPass...); code != 0 {
		t.Fatalf("baseline: PASS_TO_PASS targets already fail:\n%s", out)
	}
	return home
}

// requireSWETooling probes the fixture's prerequisites and reports BLOCKED
// rather than failing where they are missing, matching C08's convention — the
// case is unrunnable there, not broken.
func requireSWETooling(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"python3", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("BLOCKED: %s not found in PATH", bin)
		}
	}
}

// sweOracles runs the three SWE-bench oracles and returns one line per
// unsatisfied one; an empty result means the task was solved. Callers decide
// severity: the fake tier fails, the live tier reports advisory.
func sweOracles(t *testing.T, home testHome) []string {
	t.Helper()
	var misses []string
	// Oracle 1 — a patch exists. The agent edits the checkout in place, so
	// SWE-bench's "patch applies to the base commit" is the same question asked
	// of the working tree: is there a diff against that commit at all.
	diff := sweGit(t, home, "diff")
	if strings.TrimSpace(diff) == "" {
		misses = append(misses, "PATCH: agent left the repository unchanged")
	} else {
		t.Logf("patch against base commit:\n%s", diff)
	}
	// PASS_TO_PASS catches a suite that was weakened; this catches one that was
	// edited into agreement, which the oracle below cannot see.
	if touched := sweTouchedTests(t, home); len(touched) > 0 {
		misses = append(misses, "TESTS-MODIFIED: agent edited the suite it was scored on: "+strings.Join(touched, ", "))
	}
	if code, out := sweUnittest(t, home, sweFailToPass); code != 0 {
		misses = append(misses, "FAIL_TO_PASS: "+sweFailToPass+" still fails: "+lastOutputLine(out))
	}
	if code, out := sweUnittest(t, home, swePassToPass...); code != 0 {
		misses = append(misses, "PASS_TO_PASS: previously passing tests now fail: "+lastOutputLine(out))
	}
	return misses
}

// sweUnittest runs unittest targets against the fixture and returns the exit
// code — the oracle — plus output for the failure message. A non-zero exit is
// an expected result here, not an error.
func sweUnittest(t *testing.T, home testHome, targets ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("python3", append([]string{"-m", "unittest", "-v"}, targets...)...)
	cmd.Dir = home.workspace
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home.state.HomeDir,
		// Keep __pycache__ out of the tree so oracle runs never show up as
		// agent-authored changes in the diff.
		"PYTHONDONTWRITEBYTECODE=1",
	}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("run unittest %v: %v\n%s", targets, err, out)
	}
	return cmd.ProcessState.ExitCode(), string(out)
}

// sweTouchedTests lists working-tree paths under tests/, whether modified,
// deleted, or newly added.
func sweTouchedTests(t *testing.T, home testHome) []string {
	t.Helper()
	var touched []string
	for _, line := range strings.Split(strings.TrimSpace(sweGit(t, home, "status", "--porcelain")), "\n") {
		if len(line) <= 3 {
			continue
		}
		path := strings.Trim(strings.TrimSpace(line[2:]), `"`)
		if strings.HasPrefix(path, "tests/") {
			touched = append(touched, path)
		}
	}
	return touched
}

// sweGit runs git against the fixture checkout with identity and signing pinned
// inline, so the fixture never depends on the developer's global git config.
func sweGit(t *testing.T, home testHome, args ...string) string {
	t.Helper()
	pinned := []string{
		"-C", home.workspace,
		"-c", "user.name=invokr-e2e",
		"-c", "user.email=e2e@invokr.test",
		"-c", "commit.gpgsign=false",
	}
	out, err := exec.Command("git", append(pinned, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// runSWEExec drives one unattended task attempt. --allow-all is what a SWE
// harness gives the agent: nobody is at the keyboard to approve the edit.
func runSWEExec(t *testing.T, home testHome, providerURL, prompt string, timeout time.Duration) clitest.Result {
	t.Helper()
	return clitest.Run(t, clitest.Options{
		State:       home.state,
		CommandArgs: []string{"--base-url=" + providerURL, "exec", "--json", "--allow-all", prompt},
		Timeout:     timeout,
		Env:         execEnv(nil),
	})
}

// sweModel resolves which model the live SWE case drives, most specific first:
// INVOKR_E2E_SWE_MODEL (this case only) > INVOKR_E2E_LIVE_MODEL (the whole live
// tier). The second rung means `make e2e-live` scores the same local model the
// rest of the live tier uses without extra configuration, and the first lets a
// SWE run point at a stronger hosted model — the expected way to raise the
// score. There is deliberately no built-in default: a SWE score is a tracked
// metric, and silently attributing one to whichever model happened to be
// configured is worse than not producing a number at all, so with neither set
// the case skips.
func sweModel(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"INVOKR_E2E_SWE_MODEL", "INVOKR_E2E_LIVE_MODEL"} {
		if model := strings.TrimSpace(os.Getenv(key)); model != "" {
			return model
		}
	}
	t.Skip("BLOCKED: set INVOKR_E2E_SWE_MODEL or INVOKR_E2E_LIVE_MODEL to name the model this SWE score belongs to")
	return ""
}

// sweTimeout reads INVOKR_E2E_SWE_TIMEOUT (any time.ParseDuration value).
func sweTimeout(t *testing.T) time.Duration {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("INVOKR_E2E_SWE_TIMEOUT"))
	if raw == "" {
		return defaultSWETimeout
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil {
		t.Fatalf("INVOKR_E2E_SWE_TIMEOUT=%q: %v", raw, err)
	}
	return timeout
}

func lastOutputLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
