//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/support/testutil/clitest"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

func TestD09_ParallelNativeCallsAndAdapterStreams(t *testing.T) {
	step := parallelD09Step()
	provider := providertest.NewScripted(t, step, providertest.ReplyStep("parallel done"))
	defer provider.Close()
	home := newHome(t, provider.URL())
	result := runExec(t, home, provider.URL(), "run parallel reads")
	if result.Err != nil {
		t.Fatalf("json exec: %v %s", result.Err, result.Stderr)
	}
	got := parseLedger(t, result.Stdout)
	var started, completed int
	for _, event := range got.Events {
		switch event["type"] {
		case "item.started":
			started++
		case "item.completed":
			completed++
		}
	}
	if started != 4 || completed != 4 || got.Completed["tool_call_count"] != float64(4) || len(got.failures()) != 0 {
		t.Fatalf("started=%d completed=%d thread=%#v failures=%#v", started, completed, got.Completed, got.failures())
	}

	plainProvider := providertest.NewScripted(t, parallelD09Step(), providertest.ReplyStep("plain done"))
	defer plainProvider.Close()
	plain := clitest.Run(t, clitest.Options{
		State:       home.state,
		CommandArgs: []string{"--base-url=" + plainProvider.URL(), "exec", "run parallel reads"},
		Timeout:     30 * time.Second,
	})
	if plain.Err != nil {
		t.Fatalf("plain exec: %v %s", plain.Err, plain.Stderr)
	}
	if strings.Contains(plain.Stdout, "tool started") || !strings.Contains(plain.Stdout, "plain done") {
		t.Fatalf("plain stdout contaminated: %q", plain.Stdout)
	}
	if !strings.Contains(plain.Stderr, "tool started:") {
		t.Fatalf("plain stderr missing tool events: %q", plain.Stderr)
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
	plainProvider.AssertClean(t)
	plainProvider.AssertExhausted(t)
}

func parallelD09Step() providertest.CompletionStep {
	return providertest.CompletionStep{ToolCalls: []providertest.CompletionToolCall{
		{ID: "d09-read-a", Type: "function", Function: providertest.CompletionToolFunction{Name: "read", Arguments: map[string]any{"paths": []string{"README.md"}}}},
		{ID: "d09-read-b", Type: "function", Function: providertest.CompletionToolFunction{Name: "read", Arguments: map[string]any{"paths": []string{"docs/architecture.md"}}}},
		{ID: "d09-pwd", Type: "function", Function: providertest.CompletionToolFunction{Name: "shell", Arguments: map[string]any{"command": "pwd"}}},
		{ID: "d09-search", Type: "function", Function: providertest.CompletionToolFunction{Name: "file_search", Arguments: map[string]any{"query": "amber-orbit"}}},
	}}
}
