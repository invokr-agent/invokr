package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// --- toolcall tier: BFCL-derived single tool-call tasks ---------------------

type toolcallTask struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
	Tool   struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"tool"`
	Expect struct {
		Name string           `json:"name"`
		Args map[string][]any `json:"args"`
	} `json:"expect"`
}

func runToolcallTier(opts options) ([]result, error) {
	dir := filepath.Join(opts.tasksDir, "toolcall")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var results []result
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var task toolcallTask
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err == nil {
			err = json.Unmarshal(data, &task)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		results = append(results, runToolcallTask(opts, task))
	}
	return results, nil
}

func runToolcallTask(opts options, task toolcallTask) (res result) {
	res = result{Tier: "toolcall", Task: task.ID, Baseline: "n/a"}
	start := time.Now()
	defer func() { res.Seconds = time.Since(start).Seconds() }()

	scratch, err := os.MkdirTemp("", "bench-"+task.ID+"-*")
	if err != nil {
		res.Harness, res.Note = "error", err.Error()
		return res
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	home, workspace := filepath.Join(scratch, "home"), filepath.Join(scratch, "ws")
	if err = newHome(home); err == nil {
		err = writeToolStub(home, task)
	}
	if err == nil {
		err = os.MkdirAll(workspace, 0o755)
	}
	if err != nil {
		res.Harness, res.Note = "error", err.Error()
		return res
	}

	events, _, err := execRun(opts, home, workspace, task.Prompt)
	res.Harness, res.Note = scoreToolCalls(task, toolCalls(events), err)

	if opts.baseline {
		call, err := bareToolCall(opts, task)
		verdict, _ := scoreToolCalls(task, call, err)
		res.Baseline = verdict
	}
	return res
}

// writeToolStub exposes the task's function as a toolbox tool whose body just
// acknowledges the call; correctness is judged on the call itself.
func writeToolStub(home string, task toolcallTask) error {
	dir := filepath.Join(home, "toolbox", task.Tool.Name)
	spec := map[string]any{
		"name":        task.Tool.Name,
		"description": task.Tool.Description,
		"example":     task.Prompt,
		"parameters":  task.Tool.Parameters,
		"policy":      map[string]any{"tiers": []string{"read_workspace"}, "approval": "auto"},
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "tool.json"), data, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "main.py"), []byte(`print('{"result":"ok"}')`+"\n"), 0o644)
}

// scoreToolCalls applies BFCL-style matching: the expected function was called
// with every parameter's value inside its allowed set ("" marks an optional
// parameter that may be omitted).
func scoreToolCalls(task toolcallTask, calls [][2]string, err error) (string, string) {
	if err != nil {
		return "error", tail(err.Error(), 120)
	}
	for _, call := range calls {
		if call[0] != task.Expect.Name {
			continue
		}
		var args map[string]any
		if json.Unmarshal([]byte(call[1]), &args) != nil {
			continue
		}
		if note := matchArgs(task.Expect.Args, args); note == "" {
			return "pass", ""
		} else if len(calls) == 1 {
			return "fail", note
		}
	}
	if len(calls) == 0 {
		return "fail", "no tool call made"
	}
	return "fail", fmt.Sprintf("wrong call: %s", calls[0][0])
}

func matchArgs(expect map[string][]any, got map[string]any) string {
	for param, allowed := range expect {
		value, present := got[param]
		matched := false
		for _, candidate := range allowed {
			if candidate == "" && !present {
				matched = true // optional parameter, omitted
				break
			}
			if present && fmt.Sprintf("%v", candidate) == fmt.Sprintf("%v", value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Sprintf("param %s=%v not in %v", param, value, allowed)
		}
	}
	return ""
}

// bareToolCall asks the raw model, with the same function advertised via the
// standard tools field, for a single completion — the no-harness baseline.
func bareToolCall(opts options, task toolcallTask) ([][2]string, error) {
	body := map[string]any{
		"model":    opts.model,
		"messages": []map[string]string{{"role": "user", "content": task.Prompt}},
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        task.Tool.Name,
				"description": task.Tool.Description,
				"parameters":  task.Tool.Parameters,
			},
		}},
	}
	message, err := chatCompletion(opts, body)
	if err != nil {
		return nil, err
	}
	var calls [][2]string
	for _, call := range message.ToolCalls {
		calls = append(calls, [2]string{call.Function.Name, call.Function.Arguments})
	}
	return calls, nil
}

// --- codeedit and terminal tiers: workspace tasks scored by check.sh --------

func runWorkspaceTier(opts options, tier string) ([]result, error) {
	// check.sh runs with the workspace as cwd, so task paths must be absolute.
	root, err := filepath.Abs(filepath.Join(opts.tasksDir, tier))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var results []result
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		results = append(results, runWorkspaceTask(opts, tier, filepath.Join(root, entry.Name())))
	}
	return results, nil
}

func runWorkspaceTask(opts options, tier, taskDir string) (res result) {
	res = result{Tier: tier, Task: filepath.Base(taskDir), Baseline: "n/a"}
	start := time.Now()
	defer func() { res.Seconds = time.Since(start).Seconds() }()

	instruction, err := os.ReadFile(filepath.Join(taskDir, "instruction.txt"))
	if err != nil {
		res.Harness, res.Note = "error", err.Error()
		return res
	}

	verdict, note := runWorkspaceVariant(opts, taskDir, func(workspace string) error {
		scratch := filepath.Dir(workspace)
		home := filepath.Join(scratch, "home")
		if err := newHome(home); err != nil {
			return err
		}
		_, _, err := execRun(opts, home, workspace, string(instruction))
		return err
	})
	res.Harness, res.Note = verdict, note

	if opts.baseline && tier == "codeedit" {
		res.Baseline, _ = runWorkspaceVariant(opts, taskDir, func(workspace string) error {
			return bareCodeEdit(opts, taskDir, workspace, string(instruction))
		})
	}
	return res
}

// runWorkspaceVariant materializes the task workspace, lets produce mutate it,
// then scores it with the task's check.sh (exit 0 = pass).
func runWorkspaceVariant(opts options, taskDir string, produce func(workspace string) error) (string, string) {
	scratch, err := os.MkdirTemp("", "bench-"+filepath.Base(taskDir)+"-*")
	if err != nil {
		return "error", err.Error()
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	workspace := filepath.Join(scratch, "ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return "error", err.Error()
	}
	if files := filepath.Join(taskDir, "files"); isDir(files) {
		if err := copyDir(files, workspace); err != nil {
			return "error", err.Error()
		}
	}
	if err := produce(workspace); err != nil {
		return "error", tail(err.Error(), 120)
	}
	return runCheck(taskDir, workspace)
}

func runCheck(taskDir, workspace string) (string, string) {
	for _, extra := range []string{"check_main.py", "tests"} {
		source := filepath.Join(taskDir, extra)
		if _, err := os.Stat(source); err == nil {
			if err := copyPath(source, filepath.Join(workspace, extra)); err != nil {
				return "error", err.Error()
			}
		}
	}
	check := exec.Command("/bin/sh", filepath.Join(taskDir, "check.sh"))
	check.Dir = workspace
	output, err := check.CombinedOutput()
	if err == nil {
		return "pass", ""
	}
	if _, ok := err.(*exec.ExitError); ok {
		return "fail", tail(string(output), 100)
	}
	return "error", err.Error()
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyDir(src, dst)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// bareCodeEdit is the no-harness baseline for edit tasks: the model sees the
// target file and instruction once and must answer with the complete new file
// in a fenced block, which replaces the file verbatim.
func bareCodeEdit(opts options, taskDir, workspace, instruction string) error {
	var meta struct {
		Target string `json:"target"`
	}
	data, err := os.ReadFile(filepath.Join(taskDir, "meta.json"))
	if err == nil {
		err = json.Unmarshal(data, &meta)
	}
	if err != nil {
		return err
	}
	current, err := os.ReadFile(filepath.Join(workspace, meta.Target))
	if err != nil {
		return err
	}
	prompt := fmt.Sprintf("%s\n\nCurrent content of %s:\n```python\n%s\n```\n\n"+
		"Reply with the complete new content of %s in a single fenced code block and nothing else.",
		instruction, meta.Target, current, meta.Target)
	message, err := chatCompletion(opts, map[string]any{
		"model":    opts.model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
	if err != nil {
		return err
	}
	code := extractFence(message.Content)
	if code == "" {
		return fmt.Errorf("no fenced code block in reply")
	}
	return os.WriteFile(filepath.Join(workspace, meta.Target), []byte(code), 0o644)
}

func extractFence(text string) string {
	_, rest, ok := strings.Cut(text, "```")
	if !ok {
		return ""
	}
	if idx := strings.IndexByte(rest, '\n'); idx >= 0 {
		rest = rest[idx+1:] // drop the language tag line
	}
	code, _, ok := strings.Cut(rest, "```")
	if !ok {
		return ""
	}
	return code
}
