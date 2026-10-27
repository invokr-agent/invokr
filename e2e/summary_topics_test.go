//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	memcore "github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// TestSummaryTopicsAreDiscarded pins a dead end in the memory summarizer.
//
// summary.MemorySummary carries Topics: the summarization prompt asks the model
// for them and ParseMemorySummary trims them and caps the list at eight — and
// then nothing reads the field. The curator persists the summary and each
// high-confidence fact as memory records and drops Topics on the floor, so
// every summarization pays tokens and validation for nothing.
//
// This case drives a real turn with summarization forced on the first response,
// hands the scripted provider a summary carrying a fact and a distinctive
// topic, and shows the summary and the fact reaching the store while the topic
// reaches neither the records nor the files under the memory dir.
func TestSummaryTopicsAreDiscarded(t *testing.T) {
	const (
		summaryText = "zzz-summary-canary durable summary"
		factText    = "zzz-fact-canary"
		topicText   = "zzz-topic-canary"
	)
	summaryJSON := `{"summary":"` + summaryText + `",` +
		`"facts":[{"subject":"project","predicate":"stores","object":"` + factText + `","confidence":0.9}],` +
		`"topics":["` + topicText + `"],"importance":0.9,"should_store":true}`

	provider := providertest.NewScripted(t,
		providertest.ReplyStep("done"),
		// The curator summarizes through the main chat provider, so the
		// summarization call takes the next step. Its content is raw JSON, not
		// a tagged reply envelope — the summarizer parses the body directly.
		providertest.CompletionStep{Content: summaryJSON},
	)
	defer provider.Close()

	home := newHome(t, provider.URL())
	rewriteConfig(t, &home, func(app *config.AppConfig) {
		// Summarize after the first stored raw event instead of the twelfth.
		app.Memory.Zerfoo.MaxRawEventsBeforeSummary = 1
		app.Memory.Zerfoo.MinImportanceToStore = 0.1
	})

	got := runInProc(t, home, provider.URL(), "say done", grantAll)
	if got.Resp.Message == "" {
		t.Fatal("expected a reply from the scripted provider")
	}
	waitForCompletions(t, provider, 2)

	// Both halves the curator persists land: the summary and the fact.
	waitForRecordContent(t, got.Services.Memory, factText)
	waitForRecordContent(t, got.Services.Memory, summaryText)
	// The topic does not, under its own query or the shared canary prefix.
	for _, query := range []string{topicText, "zzz-topic", "canary"} {
		for _, rec := range recallRecords(t, got.Services.Memory, query) {
			if strings.Contains(rec.Content, topicText) {
				t.Fatalf("topic reached a memory record (%s): Topics now has a consumer", rec.Kind)
			}
		}
	}
	if fileTreeContains(home.state.MemoryDir, topicText) {
		t.Fatal("topic reached the memory store on disk: Topics now has a consumer, update the finding")
	}
	t.Logf("OBSERVATION: summary and fact stored; topic %q parsed, capped at eight, and discarded", topicText)
}

func waitForCompletions(t *testing.T, provider *providertest.Server, want int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if len(provider.Snapshot().CompletionRequests) >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("provider saw %d completions, want %d: summarization never ran",
		len(provider.Snapshot().CompletionRequests), want)
}

// memoryRecaller is the slice of the memory manager these assertions need.
type memoryRecaller interface {
	Recall(context.Context, memcore.Query) ([]memcore.Hit, error)
}

// waitForRecordContent polls memory until a record containing needle lands.
func waitForRecordContent(t *testing.T, manager memoryRecaller, needle string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, rec := range recallRecords(t, manager, needle) {
			if strings.Contains(rec.Content, needle) {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no memory record ever contained %q", needle)
}

func recallRecords(t *testing.T, manager memoryRecaller, query string) []memcore.Record {
	t.Helper()
	hits, err := manager.Recall(context.Background(), memcore.Query{Query: query, Limit: 50})
	if err != nil {
		t.Fatalf("recall %q: %v", query, err)
	}
	records := make([]memcore.Record, 0, len(hits))
	for _, hit := range hits {
		records = append(records, hit.Record)
	}
	return records
}

func fileTreeContains(root, needle string) bool {
	found := false
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || found {
			return nil //nolint:nilerr // an unreadable file cannot contain the needle
		}
		data, readErr := os.ReadFile(path)
		if readErr == nil && strings.Contains(string(data), needle) {
			found = true
		}
		return nil
	})
	return found
}
