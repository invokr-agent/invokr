package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Case classes: C/D/R/A come from the manual test plan, S from the SWE task
// cases in e2e/swe_test.go.
var caseName = regexp.MustCompile(`^Test(?:Live)?((?:C|D|R|A|S)\d{2}[a-z]?)_`)
var advisoryLine = regexp.MustCompile(`ADVISORY\[((?:C|D|R|A|S)\d{2}[a-z]?)\]\s+([A-Z-]+):\s*(.*)`)

type testEvent struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
	Output string `json:"Output"`
}

type result struct {
	State   string
	Finding string
}

func main() {
	out := flag.String("out", "", "report output path (stdout when empty)")
	tier := flag.String("tier", "fake", "expected case catalog: fake or live")
	flag.Parse()
	failed, err := run(*out, *tier)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if failed {
		os.Exit(1)
	}
}

// run writes the report to out (stdout when empty) and reports whether any case failed.
func run(out, tier string) (bool, error) {
	expected, err := expectedCases(tier)
	if err != nil {
		return false, err
	}
	report, failed, err := buildReportFor(os.Stdin, expected)
	if err == nil && out == "" {
		_, err = io.WriteString(os.Stdout, report)
	} else if err == nil {
		err = os.WriteFile(out, []byte(report), 0o600)
	}
	return failed, err
}

func buildReportFor(in io.Reader, expected []string) (string, bool, error) {
	results := map[string]result{}
	output := map[string]string{}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event testEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return "", false, fmt.Errorf("decode go test event: %w", err)
		}
		consumeEvent(results, output, event)
	}
	if err := scanner.Err(); err != nil {
		return "", false, err
	}
	for _, id := range expected {
		if _, ok := results[id]; !ok {
			results[id] = result{State: "FAIL", Finding: "missing test result"}
		}
	}
	return render(results), hasFailure(results), nil
}

func consumeEvent(results map[string]result, output map[string]string, event testEvent) {
	if event.Action == "fail" && event.Test == "" {
		finding := lastLine(event.Output)
		if finding == "" {
			finding = lastLine(output["PACKAGE"])
		}
		mergeResult(results, "PACKAGE", result{State: "FAIL", Finding: finding})
	}
	for _, match := range advisoryLine.FindAllStringSubmatch(event.Output, -1) {
		state := match[2]
		if strings.Contains(match[3], "CANARY-DISCLOSED") {
			state = "FAIL"
		}
		mergeResult(results, match[1], result{State: state, Finding: match[3]})
	}
	id := testID(event.Test)
	if id == "" {
		if event.Action == "output" {
			output["PACKAGE"] += event.Output
		}
		return
	}
	output[id] += event.Output
	switch event.Action {
	case "pass":
		mergeResult(results, id, result{State: "PASS"})
	case "fail":
		mergeResult(results, id, result{State: "FAIL", Finding: lastLine(output[id])})
	case "skip":
		state, finding := skipState(output[id])
		mergeResult(results, id, result{State: state, Finding: finding})
	}
}

func expectedCases(tier string) ([]string, error) {
	if tier != "fake" && tier != "live" {
		return nil, fmt.Errorf("unknown tier %q", tier)
	}
	out, err := exec.Command("go", "test", "-tags", "e2e", "-list", "^Test", "github.com/invokr-agent/invokr/e2e").Output()
	if err != nil {
		return nil, fmt.Errorf("discover e2e tests: %w", err)
	}
	return casesFromTestList(string(out), tier), nil
}

func casesFromTestList(list, tier string) []string {
	seen := map[string]bool{}
	for _, name := range strings.Fields(list) {
		match := caseName.FindStringSubmatch(name)
		if len(match) != 2 || strings.HasPrefix(name, "TestLive") != (tier == "live") {
			continue
		}
		seen[match[1]] = true
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func mergeResult(results map[string]result, id string, next result) {
	current, ok := results[id]
	if !ok || stateRank(next.State) > stateRank(current.State) {
		results[id] = next
	}
}

func stateRank(state string) int {
	switch state {
	case "FAIL":
		return 6
	case "BLOCKED":
		return 5
	case "INJECTION-FOLLOWED":
		return 4
	case "KNOWN", "SELECTION":
		return 3
	case "PASS":
		return 2
	default:
		return 1
	}
}

func testID(name string) string {
	match := caseName.FindStringSubmatch(strings.Split(name, "/")[0])
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func skipState(text string) (string, string) {
	for _, state := range []string{"BLOCKED", "SELECTION", "KNOWN"} {
		if strings.Contains(text, state+":") {
			return state, lastLine(text)
		}
	}
	return "BLOCKED", lastLine(text)
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

func hasFailure(results map[string]result) bool {
	for _, item := range results {
		if item.State == "FAIL" {
			return true
		}
	}
	return false
}

func render(results map[string]result) string {
	ids := make([]string, 0, len(results))
	counts := map[string]int{}
	for id, item := range results {
		ids = append(ids, id)
		counts[item.State]++
	}
	sort.Strings(ids)
	var b strings.Builder
	fmt.Fprintf(&b, "# Invokr — E2E Test Run, %s\n\n", time.Now().Format("2006-01-02"))
	fmt.Fprintf(&b, "## Environment\n\n- Go: %s\n- OS/sandbox: %s/%s\n\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	b.WriteString("## Summary\n\n| State | Count |\n|---|---:|\n")
	for _, state := range []string{"PASS", "FAIL", "BLOCKED", "SELECTION", "KNOWN", "INJECTION-FOLLOWED"} {
		fmt.Fprintf(&b, "| %s | %d |\n", state, counts[state])
	}
	b.WriteString("\n## Results\n\n| ID | State | Natural tool selected? | Evidence | Finding |\n|---|---|---|---|---|\n")
	for _, id := range ids {
		item := results[id]
		fmt.Fprintf(&b, "| %s | %s | — | go test -json | %s |\n", id, item.State, tableText(item.Finding))
	}
	b.WriteString("\n## New issues\n\n## Artifacts and cleanup\n")
	return b.String()
}

func tableText(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "|", "\\|"), "\n", " ")
}
