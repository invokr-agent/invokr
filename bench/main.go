// Command bench runs a small, deterministic benchmark suite against a live
// OpenAI-compatible provider, measuring the Invokr harness and the bare model
// side by side. Tasks live in antonikliment/test-bench (see bench/README.md);
// scoring is exact/AST/unit-test based — no LLM judge. Results print as a
// table and land in a JSON file for tracking over time.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type options struct {
	baseURL  string
	model    string
	bin      string
	tasksDir string
	tiers    string
	baseline bool
	out      string
	timeout  time.Duration
}

type result struct {
	Tier     string  `json:"tier"`
	Task     string  `json:"task"`
	Harness  string  `json:"harness"`            // pass | fail | error
	Baseline string  `json:"baseline,omitempty"` // pass | fail | error | n/a
	Seconds  float64 `json:"seconds"`
	Note     string  `json:"note,omitempty"`
}

func main() {
	opts := options{}
	flag.StringVar(&opts.baseURL, "url", "http://localhost:8080", "OpenAI-compatible base URL")
	flag.StringVar(&opts.model, "model", "", "model id sent to the provider (required)")
	flag.StringVar(&opts.bin, "bin", "invokr-dev", "invokr binary to drive")
	flag.StringVar(&opts.tasksDir, "tasks", defaultTasksDir(), "vendored tasks directory")
	flag.StringVar(&opts.tiers, "tiers", "toolcall,codeedit,terminal", "comma-separated tiers to run")
	flag.BoolVar(&opts.baseline, "baseline", true, "also run each task as a bare model call")
	flag.StringVar(&opts.out, "out", "bench-results.json", "results JSON path")
	flag.DurationVar(&opts.timeout, "timeout", 5*time.Minute, "per-run timeout")
	flag.Parse()
	if opts.model == "" {
		fmt.Fprintln(os.Stderr, "-model is required")
		os.Exit(2)
	}

	var results []result
	for _, tier := range strings.Split(opts.tiers, ",") {
		var runs []result
		var err error
		switch strings.TrimSpace(tier) {
		case "toolcall":
			runs, err = runToolcallTier(opts)
		case "codeedit":
			runs, err = runWorkspaceTier(opts, "codeedit")
		case "terminal":
			runs, err = runWorkspaceTier(opts, "terminal")
		default:
			err = fmt.Errorf("unknown tier %q", tier)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "tier %s: %v\n", tier, err)
			os.Exit(1)
		}
		results = append(results, runs...)
	}

	report(results, opts)
}

func defaultTasksDir() string {
	if exe, err := os.Executable(); err == nil {
		if dir := filepath.Join(filepath.Dir(exe), "tasks"); isDir(dir) {
			return dir
		}
	}
	return "tasks"
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

type agg struct{ pass, fail, err, bpass, btotal int }

// tally counts harness and baseline verdicts per tier.
func tally(results []result) map[string]*agg {
	byTier := map[string]*agg{}
	for _, r := range results {
		a := byTier[r.Tier]
		if a == nil {
			a = &agg{}
			byTier[r.Tier] = a
		}
		switch r.Harness {
		case "pass":
			a.pass++
		case "fail":
			a.fail++
		default:
			a.err++
		}
		if r.Baseline != "n/a" && r.Baseline != "" {
			a.btotal++
			if r.Baseline == "pass" {
				a.bpass++
			}
		}
	}
	return byTier
}

func report(results []result, opts options) {
	fmt.Printf("\n%-9s %-32s %-8s %-9s %6s  %s\n", "TIER", "TASK", "HARNESS", "BASELINE", "SECS", "NOTE")
	for _, r := range results {
		fmt.Printf("%-9s %-32s %-8s %-9s %6.1f  %s\n", r.Tier, r.Task, r.Harness, r.Baseline, r.Seconds, r.Note)
	}
	fmt.Println()
	for tier, a := range tally(results) {
		total := a.pass + a.fail + a.err
		line := fmt.Sprintf("%s: harness %d/%d", tier, a.pass, total)
		if a.btotal > 0 {
			line += fmt.Sprintf(" · baseline %d/%d", a.bpass, a.btotal)
		}
		if a.err > 0 {
			line += fmt.Sprintf(" · %d errors", a.err)
		}
		fmt.Println(line)
	}
	payload := map[string]any{
		"model": opts.model, "when": time.Now().UTC().Format(time.RFC3339), "results": results,
	}
	data, _ := json.MarshalIndent(payload, "", " ")
	if err := os.WriteFile(opts.out, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", opts.out, err)
	}
	fmt.Printf("\nresults written to %s\n", opts.out)
}
