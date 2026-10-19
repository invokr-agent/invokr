//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/agentprompt"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

func TestD06_ContextWarningAndCompaction(t *testing.T) {
	const (
		original = "d06-original-request"
		summary  = "d06-marked-summary"
	)
	home := newHome(t, "")
	home.app.Provider.ContextWindowTokens = 4_400 // usable window; no summarizer headroom comes off it (#577)
	for i := 1; i <= 3; i++ {
		mustWrite(t, filepath.Join(home.workspace, "d06-large-"+string(rune('0'+i))+".txt"), strings.Repeat(string(rune('a'+i)), 6_000))
	}
	provider := providertest.New(t, []providertest.CompletionStep{
		providertest.NativeToolCallStep("d06-read-1", "read", map[string]any{"paths": []string{"d06-large-1.txt"}}),
		providertest.NativeToolCallStep("d06-read-2", "read", map[string]any{"paths": []string{"d06-large-2.txt"}}),
		providertest.NativeToolCallStep("d06-read-3", "read", map[string]any{"paths": []string{"d06-large-3.txt"}}),
		{Content: summary},
		providertest.ReplyStep("d06-done"),
	}, providertest.WithContextWindow(4_400))
	defer provider.Close()

	result := runInProc(t, home, provider.URL(), original, grantAll)
	if result.Resp.Message != "d06-done" {
		requests := provider.Snapshot().CompletionRequests
		counts := make([]int, len(requests))
		for i := range requests {
			counts[i] = len(requests[i].Messages)
		}
		t.Fatalf("final response = %q request message counts=%v", result.Resp.Message, counts)
	}

	requests := provider.Snapshot().CompletionRequests
	logRequestSizes(t, requests)
	if len(requests) != 5 {
		t.Fatalf("completion requests = %d, want 5", len(requests))
	}
	var summaryPrompts, compacted int
	for _, req := range requests {
		text := requestText(req)
		if strings.Contains(text, "Summarize this quoted conversation segment compactly") {
			summaryPrompts++
		}
		if strings.Contains(text, "assistant:"+agentprompt.SummaryLabel+strconv.Quote(summary)) {
			compacted++
			if !strings.Contains(text, original) {
				t.Fatal("compacted request lost original user request")
			}
			encoded, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			for _, newest := range []string{"d06-read-2", "d06-read-3"} {
				if !strings.Contains(string(encoded), newest) {
					t.Fatalf("compacted request lost recent message %q", newest)
				}
			}
		}
	}
	if summaryPrompts != 1 || compacted != 1 {
		t.Fatalf("summary requests = %d, compacted requests = %d; want 1 each", summaryPrompts, compacted)
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

func requestText(req providertest.CompletionRequest) string {
	var out strings.Builder
	for _, message := range req.Messages {
		out.WriteString(message.Role)
		out.WriteByte(':')
		out.WriteString(toText(message.Content))
		out.WriteByte('\n')
	}
	return out.String()
}

func toText(value any) string {
	switch value := value.(type) {
	case string:
		return value
	default:
		return ""
	}
}

// DEBUG (throwaway): per-request message roles and byte sizes, to compare the
// GitHub runner against local runs.
func logRequestSizes(t *testing.T, requests []providertest.CompletionRequest) {
	for i, req := range requests {
		tools, _ := json.Marshal(req.Tools)
		var parts []string
		for _, m := range req.Messages {
			b, _ := json.Marshal(m)
			parts = append(parts, fmt.Sprintf("%s:%d", m.Role, len(b)))
		}
		t.Logf("DEBUG req %d tools=%d msgs=%v", i, len(tools), parts)
		if i == 0 && len(req.Messages) > 0 {
			b, _ := json.Marshal(req.Messages[0])
			t.Logf("DEBUG system=%s", b)
		}
	}
}
