package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/clients"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestProviderContextResetsFallbackAndRejectsStaleMetadata(t *testing.T) {
	state := NewProviderContext(64000, "model-a")
	if !state.ApplyMetadata("model-a", clients.ProviderMetadata{ContextWindowTokens: 128000}) {
		t.Fatal("initial model metadata was not applied")
	}
	state.SetModel("model-b")
	if got := state.ContextWindowTokens(); got != 64000 {
		t.Fatalf("window after switch = %d, want configured fallback 64000", got)
	}
	if state.ApplyMetadata("model-a", clients.ProviderMetadata{ContextWindowTokens: 256000}) {
		t.Fatal("stale model metadata was applied")
	}
	if !state.ApplyMetadata("model-b", clients.ProviderMetadata{ContextWindowTokens: 32768}) {
		t.Fatal("active model metadata was not recorded")
	}

	unknown := NewProviderContext(0, "model-a")
	unknown.ApplyMetadata("model-a", clients.ProviderMetadata{ContextWindowTokens: 128000})
	wantUnknown := int64(128000)
	if got := unknown.ContextWindowTokens(); got != wantUnknown {
		t.Fatalf("discovered window with nothing configured = %d, want %d", got, wantUnknown)
	}
	unknown.SetModel("model-b")
	if got := unknown.ContextWindowTokens(); got != 0 {
		t.Fatalf("unknown fallback after switch = %d, want 0", got)
	}
}

func TestRunnerRefreshProviderMetadataRetriesForModel(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("model"); got != "router-model" {
			t.Fatalf("model query = %q, want router-model", got)
		}
		if attempts.Add(1) == 1 {
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":32768}}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := NewProviderContext(0, "router-model")
	runner := &Runner{
		provider:          clients.NewClient(server.URL),
		providerContext:   state,
		backgroundContext: ctx,
		logger:            zap.NewNop(),
	}
	runner.refreshProviderMetadata("router-model")

	want := int64(32768)
	deadline := time.Now().Add(2 * time.Second)
	for state.ContextWindowTokens() != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := state.ContextWindowTokens(); got != want || attempts.Load() != 2 {
		t.Fatalf("context=%d attempts=%d, want %d after 2", got, attempts.Load(), want)
	}
}

// A model-swapping router answers /props before the model is resident, with
// n_ctx 0. That is not a failure, so the old probe accepted it and left the
// window unset for the whole run — a 131072-token server reported as unknown
// in every ledger. Keep probing until a real window arrives.
func TestRunnerProbeWaitsForRouterToLoadTheModel(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			_, _ = w.Write([]byte(`{"role":"router","default_generation_settings":{"n_ctx":0}}`))
			return
		}
		_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":131072}}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := NewProviderContext(0, "router-model")
	runner := &Runner{
		provider:          clients.NewClient(server.URL),
		providerContext:   state,
		backgroundContext: ctx,
		logger:            zap.NewNop(),
	}
	runner.refreshProviderMetadata("router-model")

	want := int64(131072)
	deadline := time.Now().Add(5 * time.Second)
	for state.ContextWindowTokens() != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := state.ContextWindowTokens(); got != want {
		t.Fatalf("window = %d after %d probes, want %d", got, attempts.Load(), want)
	}
}

// A configured window is a pin: a provider reporting a larger one must not
// raise it above the pin (minus the reserved headroom), or the compaction
// threshold silently moves to the provider's limit (marathon M1: 32768
// configured, 131072 discovered, no compaction).
func TestProviderContextConfiguredWindowPinsOverDiscovery(t *testing.T) {
	pinned := NewProviderContext(32768, "model-a")
	if !pinned.ApplyMetadata("model-a", clients.ProviderMetadata{ContextWindowTokens: 131072}) {
		t.Fatal("metadata probe result was not recorded")
	}
	wantPinned := int64(32768)
	if got := pinned.ContextWindowTokens(); got != wantPinned {
		t.Fatalf("window = %d, want the configured pin %d", got, wantPinned)
	}

	discovered := NewProviderContext(0, "model-a")
	if !discovered.ApplyMetadata("model-a", clients.ProviderMetadata{ContextWindowTokens: 131072}) {
		t.Fatal("metadata probe result was not recorded")
	}
	wantDiscovered := int64(131072)
	if got := discovered.ContextWindowTokens(); got != wantDiscovered {
		t.Fatalf("window = %d, want the discovered 131072 minus headroom %d", got, wantDiscovered)
	}
}

// SetModel resets to the configured fallback, not to whatever the previous
// model discovered; a later probe for the new model must still be pinned.
func TestProviderContextConfiguredWindowStaysPinnedAcrossSetModel(t *testing.T) {
	pinned := NewProviderContext(32768, "model-a")
	if !pinned.ApplyMetadata("model-a", clients.ProviderMetadata{ContextWindowTokens: 131072}) {
		t.Fatal("metadata probe result was not recorded")
	}
	pinned.SetModel("model-b")
	if got := pinned.ContextWindowTokens(); got != 32768 {
		t.Fatalf("window right after SetModel = %d, want the configured pin 32768", got)
	}
	if !pinned.ApplyMetadata("model-b", clients.ProviderMetadata{ContextWindowTokens: 200000}) {
		t.Fatal("metadata probe result for the new model was not recorded")
	}
	wantAfterDiscovery := int64(32768)
	if got := pinned.ContextWindowTokens(); got != wantAfterDiscovery {
		t.Fatalf("window after discovery on the new model = %d, want the configured pin %d", got, wantAfterDiscovery)
	}
}

// #507: a pin that meets or exceeds the discovered engine window leaves no
// slack for the #501 prefetch gate, so every compaction ran synchronously.
// Reserve headroom automatically and warn once about it.
// No summarizer headroom comes off the window (#577): a pin that meets the
// engine window is used as is, and a pin below it is kept, not clamped.
func TestProviderContextUsesTheFullWindow(t *testing.T) {
	state := NewProviderContext(65536, "model-a")
	if !state.ApplyMetadata("model-a", clients.ProviderMetadata{ContextWindowTokens: 65536}) {
		t.Fatal("metadata probe result was not recorded")
	}
	if got := state.ContextWindowTokens(); got != 65536 {
		t.Fatalf("effective window = %d, want the full 65536", got)
	}
	below := NewProviderContext(16384, "model-b")
	below.ApplyMetadata("model-b", clients.ProviderMetadata{ContextWindowTokens: 131072})
	if got := below.ContextWindowTokens(); got != 16384 {
		t.Fatalf("window with pin below engine window = %d, want the pin 16384", got)
	}
}

// #495: a pinned window below the recommended 64k floor warns once, from
// SetLogger/SetModel, independent of ApplyMetadata.
func TestProviderContextWarnsOnceBelowRecommendedMinimum(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	state := NewProviderContext(32768, "model-a")
	state.SetLogger(zap.New(core))

	state.ApplyMetadata("model-a", clients.ProviderMetadata{ContextWindowTokens: 131072})
	want := int64(32768)
	if got := state.ContextWindowTokens(); got != want {
		t.Fatalf("effective window = %d, want the configured pin %d", got, want)
	}
	state.ApplyMetadata("model-a", clients.ProviderMetadata{ContextWindowTokens: 131072})
	entries := logs.FilterMessage("context window below the recommended minimum of 64k tokens").All()
	if len(entries) != 1 {
		t.Fatalf("got %d below-minimum warnings, want exactly 1", len(entries))
	}

	// A pin at or above the floor must not warn, even once headroom trims
	// the effective window under it.
	roomy := NewProviderContext(65536, "model-b")
	core2, logs2 := observer.New(zap.WarnLevel)
	roomy.SetLogger(zap.New(core2))
	roomy.ApplyMetadata("model-b", clients.ProviderMetadata{ContextWindowTokens: 200000})
	if entries := logs2.FilterMessage("context window below the recommended minimum of 64k tokens").All(); len(entries) != 0 {
		t.Fatalf("got %d below-minimum warnings for a 65536 pin, want 0", len(entries))
	}
}

// #495: the below-64k warning must fire even when /props metadata never
// arrives - previously it only ever fired from inside ApplyMetadata.
func TestProviderContextWarnsOnPinBelowMinimumWithoutMetadata(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	state := NewProviderContext(32768, "model-a")
	state.SetLogger(zap.New(core))

	entries := logs.FilterMessage("context window below the recommended minimum of 64k tokens").All()
	if len(entries) != 1 {
		t.Fatalf("got %d below-minimum warnings with no metadata ever applied, want exactly 1", len(entries))
	}
}
