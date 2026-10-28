//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/support/testutil/clitest"
	"github.com/invokr-agent/invokr/support/testutil/providertest"
)

func TestD08_ClientAdapterContracts(t *testing.T) {
	home := newHome(t, "")

	plainProvider := d08Provider(t, "plain done")
	plain := d08Run(t, home, plainProvider.URL(), []string{"exec", "-"}, "d08 plain stdin")
	if plain.Err != nil || !strings.Contains(plain.Stdout, "plain done") || strings.Contains(plain.Stdout, "tool started:") {
		t.Fatalf("plain adapter: err=%v stdout=%q stderr=%q", plain.Err, plain.Stdout, plain.Stderr)
	}
	if !strings.Contains(plain.Stderr, "tool started: read") || !strings.Contains(plain.Stderr, "tool failed: shell") {
		t.Fatalf("plain tool events not on stderr: %q", plain.Stderr)
	}
	d08AssertProvider(t, plainProvider)

	jsonProvider := d08Provider(t, "json done")
	jsonRun := d08Run(t, home, jsonProvider.URL(), []string{"exec", "--json", "d08-resume-canary-77"}, "")
	if jsonRun.Err != nil {
		t.Fatalf("json adapter: %v %s", jsonRun.Err, jsonRun.Stderr)
	}
	ledger := parseLedger(t, jsonRun.Stdout)
	var started int
	for _, event := range ledger.Events {
		if event["type"] == "item.started" {
			started++
		}
	}
	failures := ledger.failures()
	if started != 2 || ledger.Completed["tool_call_count"] != float64(2) || len(failures) != 1 || failures[0].Tool != "shell" || failures[0].Kind != "denied" {
		t.Fatalf("json ledger: started=%d completed=%#v failures=%#v", started, ledger.Completed, failures)
	}
	d08AssertProvider(t, jsonProvider)

	failProvider := d08Provider(t, "failed tools reported")
	failRun := d08Run(t, home, failProvider.URL(), []string{"exec", "--json", "--fail-on-tool-error", "d08 fail"}, "")
	if failRun.ExitCode != 1 || strings.Contains(failRun.Stderr, "Usage:") {
		t.Fatalf("fail-on-tool-error: exit=%d stderr=%q", failRun.ExitCode, failRun.Stderr)
	}
	parseLedger(t, failRun.Stdout)
	d08AssertProvider(t, failProvider)

	schema := filepath.Join(home.state.HomeDir, "d08-schema.json")
	if err := os.WriteFile(schema, []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	schemaProvider := d08Provider(t, `{"codename":"amber-orbit"}`)
	schemaRun := d08Run(t, home, schemaProvider.URL(), []string{"exec", "--output-schema", schema, "d08 schema"}, "")
	var structured map[string]any
	if schemaRun.Err != nil || json.Unmarshal([]byte(strings.TrimSpace(schemaRun.Stdout)), &structured) != nil || strings.Contains(schemaRun.Stdout, "Tool attempt failures") {
		t.Fatalf("output schema: err=%v stdout=%q stderr=%q", schemaRun.Err, schemaRun.Stdout, schemaRun.Stderr)
	}
	d08AssertProvider(t, schemaProvider)

	sessions := filepath.Join(home.state.MemoryDir, "sessions")
	entries, err := os.ReadDir(sessions)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		path := filepath.Join(sessions, entry.Name())
		data, readErr := os.ReadFile(path)
		if readErr == nil && strings.Contains(string(data), "d08-resume-canary-77") {
			future := time.Now().Add(time.Minute)
			if err := os.Chtimes(path, future, future); err != nil {
				t.Fatal(err)
			}
		}
	}

	resumeProvider := d08Provider(t, "resume done")
	resumeRun := d08Run(t, home, resumeProvider.URL(), []string{"exec", "--last", "continue"}, "")
	if resumeRun.Err != nil || !strings.Contains(resumeRun.Stdout, "resume done") {
		t.Fatalf("resume --last: err=%v stdout=%q stderr=%q", resumeRun.Err, resumeRun.Stdout, resumeRun.Stderr)
	}
	requests := resumeProvider.Snapshot()
	requestJSON, err := json.Marshal(requests.CompletionRequests)
	if err != nil || len(requests.CompletionRequests) == 0 || !strings.Contains(string(requestJSON), "d08-resume-canary-77") {
		t.Fatalf("resume request omitted prior transcript: %#v", requests)
	}
	d08AssertProvider(t, resumeProvider)
}

func d08Provider(t *testing.T, reply string) *providertest.Server {
	t.Helper()
	provider := providertest.NewScripted(t, providertest.CompletionStep{ToolCalls: []providertest.CompletionToolCall{
		{ID: "d08-read", Type: "function", Function: providertest.CompletionToolFunction{Name: "read", Arguments: map[string]any{"paths": []string{"README.md"}}}},
		{ID: "d08-deny", Type: "function", Function: providertest.CompletionToolFunction{Name: "shell", Arguments: map[string]any{"command": "git", "args": []string{"branch", "d08-denied"}}}},
	}}, providertest.ReplyStep(reply))
	t.Cleanup(provider.Close)
	return provider
}

func d08Run(t *testing.T, home testHome, providerURL string, args []string, input string) clitest.Result {
	t.Helper()
	return clitest.Run(t, clitest.Options{
		State: home.state, Input: input, Timeout: 30 * time.Second,
		CommandArgs: append([]string{"--base-url=" + providerURL}, args...),
		Env:         map[string]string{"INVOKR_TOOLBOX_ALLOW_UNSANDBOXED": "1"},
	})
}

func d08AssertProvider(t *testing.T, provider *providertest.Server) {
	t.Helper()
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}
