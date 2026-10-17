package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// execRun drives `invokr exec --json` with an isolated home and workspace and
// returns the parsed JSONL events. The harness under test is the real binary,
// same as a user runs.
func execRun(opts options, home, workspace, prompt string) ([]map[string]any, string, error) {
	args := []string{"--base-url=" + opts.baseURL, "--model=" + opts.model, "exec", "--json", "--allow-all", prompt}
	cmd := exec.Command(opts.bin, args...)
	cmd.Dir = workspace
	// Tell the harness about the wall clock so its wrap-up path (warning at
	// 75%, tool-free salvage ending near 1.3x the budget) fires before the
	// hard kill below, and a slow run still ends with a ledger.
	runTimeout := (opts.timeout * 7 / 10).Round(time.Second)
	cmd.Env = append(os.Environ(), "INVOKR_HOME="+home, "INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1", "INVOKR_AGENT_RUN_TIMEOUT="+runTimeout.String())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	timer := time.AfterFunc(opts.timeout, func() { _ = cmd.Process.Kill() })
	err := cmd.Run()
	timer.Stop()
	events, perr := parseEvents(stdout.String())
	if perr != nil && err == nil {
		err = perr
	}
	if err != nil {
		err = fmt.Errorf("%w; stderr tail: %s", err, tail(stderr.String(), 300))
	}
	return events, finalMessage(events), err
}

func parseEvents(stdout string) ([]map[string]any, error) {
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return events, fmt.Errorf("bad JSONL line: %s", tail(line, 200))
		}
		events = append(events, event)
	}
	return events, nil
}

func finalMessage(events []map[string]any) string {
	for _, event := range events {
		if event["type"] == "thread.completed" {
			message, _ := event["message"].(string)
			return message
		}
	}
	return ""
}

// toolCalls extracts (name, arguments-JSON) pairs from completed tool items.
func toolCalls(events []map[string]any) [][2]string {
	var calls [][2]string
	for _, event := range events {
		if event["type"] != "item.completed" {
			continue
		}
		item, _ := event["item"].(map[string]any)
		name, _ := item["name"].(string)
		args, _ := item["arguments"].(string)
		if name != "" {
			calls = append(calls, [2]string{name, args})
		}
	}
	return calls
}

// newHome creates an empty isolated invokr home; exec's silent first-run path
// fills in a default config. Approvals come from --allow-all (the runs live in
// throwaway temp workspaces), and INVOKR_TOOLBOX_ALLOW_UNSANDBOXED lets the
// toolcall stubs run without docker.
func newHome(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return os.CopyFS(dst, os.DirFS(src))
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
