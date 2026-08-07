package lsp

import (
	"context"
	"sync"
	"time"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// clientHandler is the client half of the connection. It answers only what a
// server needs to get started and collects published diagnostics; every other
// method falls through to UnimplementedClient's "method not found", which
// servers treat as an unsupported optional feature.
type clientHandler struct {
	protocol.UnimplementedClient

	mu      sync.Mutex
	byURI   map[uri.URI][]protocol.Diagnostic
	changed chan struct{}
}

func newClientHandler() *clientHandler {
	return &clientHandler{byURI: map[uri.URI][]protocol.Diagnostic{}, changed: make(chan struct{})}
}

// PublishDiagnostics records the server's latest verdict on one document and
// wakes anyone waiting on it.
func (h *clientHandler) PublishDiagnostics(_ context.Context, params *protocol.PublishDiagnosticsParams) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.byURI[params.URI] = params.Diagnostics
	close(h.changed)
	h.changed = make(chan struct{})
	return nil
}

// forget drops the recorded verdict for a document, so a wait after the file
// changed blocks for the server's next publish instead of returning the verdict
// on the previous text.
func (h *clientHandler) forget(docURI uri.URI) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.byURI, docURI)
}

// Configuration replies with one empty settings object per requested section:
// no custom settings, but a well-formed answer, which servers such as gopls
// require before they will index anything.
func (h *clientHandler) Configuration(_ context.Context, params *protocol.ConfigurationParams) ([]protocol.LSPAny, error) {
	settings := make([]protocol.LSPAny, len(params.Items))
	for i := range settings {
		settings[i] = protocol.LSPAny("{}")
	}
	return settings, nil
}

// The remaining overrides acknowledge notifications the spike ignores. They
// exist so a server does not treat a "method not found" reply as a fault.

func (h *clientHandler) RegisterCapability(context.Context, *protocol.RegistrationParams) error {
	return nil
}

func (h *clientHandler) UnregisterCapability(context.Context, *protocol.UnregistrationParams) error {
	return nil
}

func (h *clientHandler) WorkDoneProgressCreate(context.Context, *protocol.WorkDoneProgressCreateParams) error {
	return nil
}

func (h *clientHandler) Progress(context.Context, *protocol.ProgressParams) error { return nil }

func (h *clientHandler) LogMessage(context.Context, *protocol.LogMessageParams) error { return nil }

func (h *clientHandler) ShowMessage(context.Context, *protocol.ShowMessageParams) error { return nil }

// wait returns the diagnostics for docURI, blocking until the server first
// publishes for it or timeout expires. A clean file still gets a publish (with
// an empty list), so a timeout means the server never answered rather than
// that there was nothing to report.
func (h *clientHandler) wait(ctx context.Context, docURI uri.URI, timeout time.Duration) ([]protocol.Diagnostic, bool) {
	deadline := time.After(timeout)
	for {
		h.mu.Lock()
		published, ok := h.byURI[docURI]
		changed := h.changed
		h.mu.Unlock()
		if ok {
			return published, true
		}
		select {
		case <-changed:
		case <-deadline:
			return nil, false
		case <-ctx.Done():
			return nil, false
		}
	}
}
