package lsp

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// fakeServer is a language server that answers the four read-path requests
// with fixed data. It lets the spike be tested without gopls installed, which
// is the reason Connect takes a stream rather than only spawning a process.
type fakeServer struct {
	protocol.UnimplementedServer

	client protocol.Client
	docURI uri.URI

	// encoding is what this server claims to speak, and offered is what the
	// client asked for — both are the subject of the position-encoding tests.
	encoding protocol.PositionEncodingKind
	offered  []protocol.PositionEncodingKind

	echoPosition bool

	mu           sync.Mutex
	sent         []string
	opens        int
	lastPosition protocol.Position
}

func (f *fakeServer) Initialize(_ context.Context, params *protocol.InitializeParams) (*protocol.InitializeResult, error) {
	if general := params.Capabilities.General; general != nil {
		f.offered = general.PositionEncodings
	}
	return &protocol.InitializeResult{
		Capabilities: protocol.ServerCapabilities{PositionEncoding: f.encoding},
	}, nil
}

func (f *fakeServer) Initialized(context.Context, *protocol.InitializedParams) error { return nil }

func (f *fakeServer) Shutdown(context.Context) error { return nil }

func (f *fakeServer) Exit(context.Context) error { return nil }

func (f *fakeServer) DidOpen(ctx context.Context, params *protocol.DidOpenTextDocumentParams) error {
	f.docURI = params.TextDocument.URI
	f.mu.Lock()
	f.opens++
	f.mu.Unlock()
	f.record(string(params.TextDocument.Text))
	return f.publish(ctx, params.TextDocument.URI, "undefined: Missing")
}

// DidChange is how the server learns a file was edited after it was opened. The
// fake records the text it was told so a test can assert it was told at all.
func (f *fakeServer) DidChange(ctx context.Context, params *protocol.DidChangeTextDocumentParams) error {
	for _, change := range params.ContentChanges {
		if whole, ok := change.(*protocol.TextDocumentContentChangeWholeDocument); ok {
			f.record(whole.Text)
		}
	}
	return f.publish(ctx, params.TextDocument.URI, "undefined: Edited")
}

func (f *fakeServer) publish(ctx context.Context, docURI uri.URI, message string) error {
	return f.client.PublishDiagnostics(ctx, &protocol.PublishDiagnosticsParams{
		URI: docURI,
		Diagnostics: []protocol.Diagnostic{{
			Range:    span(6, 1, 6, 9),
			Severity: protocol.DiagnosticSeverityError,
			Source:   protocol.NewOptional("fake"),
			Message:  protocol.String(message),
		}},
	})
}

func (f *fakeServer) record(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
}

func (f *fakeServer) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

func (f *fakeServer) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

func (f *fakeServer) Definition(_ context.Context, params *protocol.DefinitionParams) (protocol.DefinitionResult, error) {
	f.mu.Lock()
	f.lastPosition = params.Position
	echo := f.echoPosition
	f.mu.Unlock()
	// Echoing the position back is how a test watches one position cross the
	// wire in both directions and checks it survives the round trip.
	if echo {
		at := protocol.Range{Start: params.Position, End: params.Position}
		return protocol.LocationSlice{{URI: params.TextDocument.URI, Range: at}}, nil
	}
	return protocol.LocationSlice{{URI: params.TextDocument.URI, Range: span(2, 5, 2, 12)}}, nil
}

func (f *fakeServer) position() protocol.Position {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastPosition
}

func (f *fakeServer) References(_ context.Context, params *protocol.ReferenceParams) ([]protocol.Location, error) {
	return []protocol.Location{
		{URI: params.TextDocument.URI, Range: span(2, 5, 2, 12)},
		{URI: params.TextDocument.URI, Range: span(9, 1, 9, 8)},
	}, nil
}

func (f *fakeServer) DocumentSymbol(_ context.Context, _ *protocol.DocumentSymbolParams) (protocol.DocumentSymbolResult, error) {
	return protocol.DocumentSymbolSlice{{
		Name:           "Widget",
		Kind:           protocol.SymbolKindStruct,
		SelectionRange: span(2, 5, 2, 11),
		Children: []protocol.DocumentSymbol{{
			Name:           "Render",
			Kind:           protocol.SymbolKindMethod,
			SelectionRange: span(4, 1, 4, 7),
		}},
	}}, nil
}

func span(startLine, startChar, endLine, endChar uint32) protocol.Range {
	return protocol.Range{
		Start: protocol.Position{Line: startLine, Character: startChar},
		End:   protocol.Position{Line: endLine, Character: endChar},
	}
}

func testServer(t *testing.T) (*Client, *fakeServer, string) {
	t.Helper()
	// UTF-8 by default, which keeps position conversion out of the way of the
	// tests that are not about it. Real servers do not: gopls and pyright both
	// answer utf-16, and TestUTF16ServerPositionsAreConverted covers that.
	return testServerWith(t, protocol.PositionEncodingKindUTF8, "package widget\n")
}

func testServerWith(t *testing.T, encoding protocol.PositionEncodingKind, fixture string) (*Client, *fakeServer, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "widget.go")
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ctx := t.Context()
	clientSide, serverSide := jsonrpc2.NewChannelStreamPair(8)
	fake := &fakeServer{encoding: encoding}
	_, serverConn, toClient := protocol.NewServer(context.WithoutCancel(ctx), fake, serverSide)
	fake.client = toClient

	client, err := Connect(ctx, clientSide, ServerConfig{Command: "fake", LanguageID: "go"}, root, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = serverConn.Close()
	})
	return client, fake, path
}

func testClient(t *testing.T) (*Client, string) {
	t.Helper()
	client, _, path := testServer(t)
	return client, path
}

func TestDefinitionReportsWorkspaceRelativeOneBasedPositions(t *testing.T) {
	client, path := testClient(t)

	found, err := client.Definition(t.Context(), path, 7, 3)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 location, got %d: %#v", len(found), found)
	}
	// The fake answers with the 0-based wire range 2:5-2:12.
	want := Location{Path: "widget.go", Line: 3, Column: 6, EndLine: 3, EndColumn: 13}
	if found[0] != want {
		t.Fatalf("expected %#v, got %#v", want, found[0])
	}
}

func TestReferencesReturnsEveryLocation(t *testing.T) {
	client, path := testClient(t)

	found, err := client.References(t.Context(), path, 7, 3)
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("expected 2 references, got %d: %#v", len(found), found)
	}
	if found[1].Line != 10 {
		t.Fatalf("expected second reference on line 10, got %d", found[1].Line)
	}
}

func TestSymbolsFlattensChildrenAndKeepsTheContainer(t *testing.T) {
	client, path := testClient(t)

	found, err := client.Symbols(t.Context(), path)
	if err != nil {
		t.Fatalf("symbols: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("expected the struct and its method, got %#v", found)
	}
	if found[0].Name != "Widget" || found[0].Kind != "struct" || found[0].Container != "" {
		t.Fatalf("unexpected parent symbol: %#v", found[0])
	}
	if found[1].Name != "Render" || found[1].Kind != "method" || found[1].Container != "Widget" {
		t.Fatalf("unexpected child symbol: %#v", found[1])
	}
}

func TestDiagnosticsWaitsForThePublishedNotification(t *testing.T) {
	client, path := testClient(t)

	found, err := client.Diagnostics(t.Context(), path, 5*time.Second)
	if err != nil {
		t.Fatalf("diagnostics: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 diagnostic, got %#v", found)
	}
	want := Diagnostic{
		Severity: "error",
		Message:  "undefined: Missing",
		Source:   "fake",
		Location: Location{Path: "widget.go", Line: 7, Column: 2, EndLine: 7, EndColumn: 10},
	}
	if found[0] != want {
		t.Fatalf("expected %#v, got %#v", want, found[0])
	}
}

func TestWaitTimesOutWhenTheServerNeverPublishesForTheDocument(t *testing.T) {
	handler := newClientHandler()
	wanted := uri.File("/workspace/wanted.go")

	// A publish for some other document must not satisfy the wait.
	if err := handler.PublishDiagnostics(t.Context(), &protocol.PublishDiagnosticsParams{
		URI: uri.File("/workspace/other.go"),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, ok := handler.wait(t.Context(), wanted, 50*time.Millisecond); ok {
		t.Fatal("expected the wait to time out")
	}

	// An empty list is still an answer: the file is clean, not unanswered.
	if err := handler.PublishDiagnostics(t.Context(), &protocol.PublishDiagnosticsParams{URI: wanted}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	published, ok := handler.wait(t.Context(), wanted, 50*time.Millisecond)
	if !ok || len(published) != 0 {
		t.Fatalf("expected an empty published list, got %#v (ok=%v)", published, ok)
	}
}

// A server binary that starts but never speaks LSP must not block the caller
// forever: Manager.client holds its lock across Start, so one such server would
// otherwise wedge every later lsp call.
func TestConnectGivesUpOnAServerThatNeverAnswersTheHandshake(t *testing.T) {
	previous := handshakeTimeout
	handshakeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { handshakeTimeout = previous })

	// No server is started on the far side, so initialize is never answered.
	clientSide, serverSide := jsonrpc2.NewChannelStreamPair(8)
	t.Cleanup(func() { _ = serverSide.Close() })

	done := make(chan error, 1)
	go func() {
		_, err := Connect(context.Background(), clientSide, ServerConfig{Command: "mute"}, t.TempDir(), nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the handshake to fail rather than succeed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect blocked past the handshake timeout")
	}
}

func TestUnknownFileIsReportedRatherThanSentToTheServer(t *testing.T) {
	client, _ := testClient(t)

	if _, err := client.Definition(t.Context(), filepath.Join(t.TempDir(), "absent.go"), 1, 1); err == nil {
		t.Fatal("expected an error for a file that does not exist")
	}
}

// The bug this guards: a file edited after it was opened kept its first-touch
// verdict, so diagnostics on a file broken by apply_patch came back clean.
func TestEditedFileIsResyncedBeforeTheNextRead(t *testing.T) {
	client, fake, path := testServer(t)
	ctx := t.Context()

	first, err := client.Diagnostics(ctx, path, 5*time.Second)
	if err != nil {
		t.Fatalf("first diagnostics: %v", err)
	}
	if first[0].Message != "undefined: Missing" {
		t.Fatalf("expected the didOpen verdict, got %q", first[0].Message)
	}

	if err := os.WriteFile(path, []byte("package widget\n\nvar Edited = 1\n"), 0o600); err != nil {
		t.Fatalf("edit fixture: %v", err)
	}

	second, err := client.Diagnostics(ctx, path, 5*time.Second)
	if err != nil {
		t.Fatalf("second diagnostics: %v", err)
	}
	if second[0].Message != "undefined: Edited" {
		t.Fatalf("expected the post-edit verdict, got %q — the stale one was reused", second[0].Message)
	}

	sent := fake.texts()
	if len(sent) != 2 {
		t.Fatalf("expected the server to be told twice (didOpen then didChange), got %d: %#v", len(sent), sent)
	}
	if !strings.Contains(sent[1], "var Edited") {
		t.Fatalf("expected didChange to carry the edited text, got %q", sent[1])
	}
}

// An unchanged file must not be re-sent on every call: the whole point of
// keeping the server alive is that repeat reads are cheap.
func TestUnchangedFileIsOnlySentOnce(t *testing.T) {
	client, fake, path := testServer(t)

	for range 3 {
		if _, err := client.Symbols(t.Context(), path); err != nil {
			t.Fatalf("symbols: %v", err)
		}
	}
	if sent := fake.texts(); len(sent) != 1 {
		t.Fatalf("expected one didOpen and no didChange, got %d: %#v", len(sent), sent)
	}
}

// The client has to ask for UTF-8: without the offer every server falls back to
// UTF-16 and every column on a non-ASCII line is wrong.
func TestClientOffersUTF8First(t *testing.T) {
	_, fake, _ := testServer(t)

	if len(fake.offered) == 0 {
		t.Fatal("expected the client to offer position encodings")
	}
	if fake.offered[0] != protocol.PositionEncodingKindUTF8 {
		t.Fatalf("expected utf-8 offered first, got %v", fake.offered)
	}
	if !slices.Contains(fake.offered, protocol.PositionEncodingKindUTF16) {
		t.Fatalf("expected utf-16 offered as the fallback, got %v", fake.offered)
	}
}

func TestNegotiatedEncodingIsRemembered(t *testing.T) {
	client, _, _ := testServerWith(t, protocol.PositionEncodingKindUTF16, "package widget\n")

	if client.encoding != protocol.PositionEncodingKindUTF16 {
		t.Fatalf("expected the server's utf-16 choice to stick, got %q", client.encoding)
	}
}

// End to end over the wire: against a UTF-16 server, a byte column on a line
// with a non-BMP rune must go out converted and come back as the same column.
func TestUTF16ServerPositionsAreConverted(t *testing.T) {
	fixture := "package widget\n" + mixedLine + "\n"
	client, fake, path := testServerWith(t, protocol.PositionEncodingKindUTF16, fixture)
	fake.echoPosition = true

	// Column 5 is the byte offset of the non-BMP rune on line 2.
	found, err := client.Definition(t.Context(), path, 2, 5)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if got := fake.position(); got.Line != 1 || got.Character != 3 {
		t.Fatalf("expected the server to receive wire 1:3, got %d:%d", got.Line, got.Character)
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 location, got %#v", found)
	}
	if found[0].Line != 2 || found[0].Column != 5 {
		t.Fatalf("expected the column to survive the round trip as 2:5, got %d:%d", found[0].Line, found[0].Column)
	}
}

// The same file against a UTF-8 server: no conversion, and the byte column is
// what goes on the wire.
func TestUTF8ServerPositionsPassThrough(t *testing.T) {
	fixture := "package widget\n" + mixedLine + "\n"
	client, fake, path := testServerWith(t, protocol.PositionEncodingKindUTF8, fixture)
	fake.echoPosition = true

	found, err := client.Definition(t.Context(), path, 2, 5)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if got := fake.position(); got.Character != 4 {
		t.Fatalf("expected the byte offset 4 on the wire, got %d", got.Character)
	}
	if found[0].Column != 5 {
		t.Fatalf("expected column 5 back, got %d", found[0].Column)
	}
}

// TestConcurrentSyncOpensOnce covers the shape the four-tool split made normal:
// two tools naming one file in the same turn. Before sync was serialized both
// calls read "not opened" and both sent didOpen at version 1 — which gopls
// rejects as a duplicate, failing whichever call lost the race. Run with -race
// this also covers the unsynchronized read of the opened map.
func TestConcurrentSyncOpensOnce(t *testing.T) {
	client, fake, path := testServer(t)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.sync(t.Context(), path); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent sync: %v", err)
	}

	// didOpen is a notification: sync returns before the server has read it.
	for deadline := time.Now().Add(2 * time.Second); fake.openCount() == 0 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if opens := fake.openCount(); opens != 1 {
		t.Fatalf("didOpen sent %d times, want exactly 1", opens)
	}
	// An unchanged file needs no didChange either: the stamp matches, so the
	// seven callers that lost the race should have sent nothing at all.
	if texts := fake.texts(); len(texts) != 1 {
		t.Fatalf("server told about the document %d times, want 1: %q", len(texts), texts)
	}
}

// TestLogStderrDrainsPastAnOverlongLine guards the reason this goroutine
// exists. bufio.Scanner gives up on a line longer than its buffer; stopping
// there leaves the pipe unread, and a server blocked writing stderr answers no
// more requests. The log is forfeit past that point, the draining is not.
func TestLogStderrDrainsPastAnOverlongLine(t *testing.T) {
	huge := strings.Repeat("x", bufio.MaxScanTokenSize+1)
	stderr := strings.NewReader(huge + "\nafter the overlong line\n")

	done := make(chan struct{})
	go func() {
		defer close(done)
		logStderr("fake", stderr)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("logStderr did not return; a real server would now be blocked writing stderr")
	}
	if left := stderr.Len(); left != 0 {
		t.Fatalf("%d bytes left unread on stderr, want the pipe fully drained", left)
	}
}

// TestSyncGivesUpWhenContextEnds is the reason the gate is a channel and not a
// Mutex. sync holds it across a write to the server's stdin, and a server that
// has stopped reading blocks that write indefinitely; Mutex.Lock has no way to
// abandon the wait, so every later call would hang past the point where the
// turn was given up on. Here the gate is held and the caller's ctx ends.
func TestSyncGivesUpWhenContextEnds(t *testing.T) {
	client, fake, path := testServer(t)

	// Stand in for a sync stuck writing to a server that stopped reading.
	<-client.syncGate
	defer func() { client.syncGate <- struct{}{} }()

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := client.sync(ctx, path)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("sync returned nil while the gate was held")
		}
		if !strings.Contains(err.Error(), "lsp sync") {
			t.Fatalf("sync error = %v, want it to name the sync that was abandoned", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sync ignored its cancelled context and kept waiting on the gate")
	}
	// The blocked caller must not have touched the server on its way out.
	if opens := fake.openCount(); opens != 0 {
		t.Fatalf("didOpen sent %d times by a caller that never held the gate, want 0", opens)
	}
}
