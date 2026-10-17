package lsp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// The line every conversion case below is measured against. Before the "λ" the
// two encodings agree; after it they diverge, and "𝛌" (outside the BMP) costs
// two UTF-16 units where it costs one of anything else.
//
//	bytes:  a=0  λ=1..2  b=3  𝛌=4..7   c=8
//	utf-16: a=0  λ=1     b=2  𝛌=3..4   c=5
const mixedLine = "aλb𝛌c"

func encodingClient(t *testing.T, kind protocol.PositionEncodingKind) *Client {
	t.Helper()
	return &Client{
		encoding: kind,
		opened:   map[uri.URI]openedDoc{},
		files:    map[string]cachedFile{},
	}
}

func TestWireToByteAcrossEncodings(t *testing.T) {
	cases := []struct {
		encoding protocol.PositionEncodingKind
		offset   uint32
		want     int
	}{
		{protocol.PositionEncodingKindUTF8, 3, 3},
		{protocol.PositionEncodingKindUTF8, 8, 8},
		{protocol.PositionEncodingKindUTF16, 0, 0},
		{protocol.PositionEncodingKindUTF16, 1, 1},
		{protocol.PositionEncodingKindUTF16, 2, 3},
		{protocol.PositionEncodingKindUTF16, 3, 4},
		{protocol.PositionEncodingKindUTF16, 5, 8},
	}
	for _, one := range cases {
		client := encodingClient(t, one.encoding)
		if got := client.wireToByte(mixedLine, one.offset); got != one.want {
			t.Errorf("%s: wireToByte(%d) = %d, want %d", one.encoding, one.offset, got, one.want)
		}
	}
}

func TestByteToWireIsTheInverse(t *testing.T) {
	for _, kind := range []protocol.PositionEncodingKind{
		protocol.PositionEncodingKindUTF8,
		protocol.PositionEncodingKindUTF16,
	} {
		client := encodingClient(t, kind)
		// Only the byte offsets that start a rune round-trip; an offset inside a
		// multi-byte rune has no wire equivalent to come back to.
		for _, offset := range []int{0, 1, 3, 4, 8} {
			wire := client.byteToWire(mixedLine, offset)
			if got := client.wireToByte(mixedLine, wire); got != offset {
				t.Errorf("%s: byte %d → wire %d → byte %d", kind, offset, wire, got)
			}
		}
	}
}

// An offset past the end of the line must clamp rather than run off the string.
func TestConversionClampsBeyondTheLine(t *testing.T) {
	client := encodingClient(t, protocol.PositionEncodingKindUTF16)
	if got := client.wireToByte(mixedLine, 999); got != len(mixedLine) {
		t.Fatalf("expected clamp to %d, got %d", len(mixedLine), got)
	}
	// a+λ+b are one unit each, 𝛌 is two, c is one: six units for the whole line.
	if got := client.byteToWire(mixedLine, 999); got != 6 {
		t.Fatalf("expected the line's full width of 6 UTF-16 units, got %d", got)
	}
}

// The regression this whole file exists for: a UTF-16 server reporting a symbol
// after a non-ASCII character used to come back with a column that pointed
// somewhere else.
func TestColumnConvertsAgainstTheOpenedText(t *testing.T) {
	client := encodingClient(t, protocol.PositionEncodingKindUTF16)
	docURI := uri.File("/workspace/widget.go")
	client.opened[docURI] = openedDoc{sourceText: newSourceText("package widget\n" + mixedLine + "\n")}

	// Wire 3 on line 1 is the "𝛌", which starts at byte 4 → 1-based column 5.
	got := client.column(docURI, protocol.Position{Line: 1, Character: 3})
	if got != 5 {
		t.Fatalf("expected column 5, got %d", got)
	}

	// And the round trip back out.
	position := client.position(docURI, 2, 5)
	if position.Line != 1 || position.Character != 3 {
		t.Fatalf("expected wire 1:3, got %d:%d", position.Line, position.Character)
	}
}

// UTF-8 is the fast path: no line lookup, so it must be right without any text.
func TestUTF8NeedsNoLineText(t *testing.T) {
	client := encodingClient(t, protocol.PositionEncodingKindUTF8)
	docURI := uri.File("/nowhere/absent.go")

	if got := client.column(docURI, protocol.Position{Line: 0, Character: 7}); got != 8 {
		t.Fatalf("expected column 8, got %d", got)
	}
	if got := client.position(docURI, 1, 8); got.Character != 7 {
		t.Fatalf("expected character 7, got %d", got.Character)
	}
}

// Locations point into the standard library and the module cache constantly,
// and those files are never opened. Conversion has to read them.
func TestLineForFallsBackToDiskAndCaches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unopened.go")
	if err := os.WriteFile(path, []byte("first\n"+mixedLine+"\nthird\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	client := encodingClient(t, protocol.PositionEncodingKindUTF16)
	docURI := uri.File(path)

	if got, ok := client.lineFor(docURI, 1); !ok || got != mixedLine {
		t.Fatalf("expected %q, got %q (known %t)", mixedLine, got, ok)
	}
	if _, ok := client.files[path]; !ok {
		t.Fatal("expected the file to be cached after a lookup")
	}
	// Served from the cache the second time, and still correct.
	if got, ok := client.lineFor(docURI, 2); !ok || got != "third" {
		t.Fatalf("expected \"third\", got %q (known %t)", got, ok)
	}
}

// A stale cache would convert against the old text, so the stat identity has to
// invalidate it.
func TestFileCacheInvalidatesOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edited.go")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	client := encodingClient(t, protocol.PositionEncodingKindUTF16)
	if got, ok := client.fileText(path); !ok || got.text != "before\n" {
		t.Fatalf("expected the original text, got %q (ok %t)", got.text, ok)
	}

	if err := os.WriteFile(path, []byte("after this edit\n"), 0o600); err != nil {
		t.Fatalf("rewrite fixture: %v", err)
	}
	if got, ok := client.fileText(path); !ok || got.text != "after this edit\n" {
		t.Fatalf("expected the re-read text, got %q (ok %t)", got.text, ok)
	}
}

func TestSourceTextLineAt(t *testing.T) {
	source := newSourceText("one\ntwo\r\nthree")
	cases := map[uint32]struct {
		want  string
		known bool
	}{0: {"one", true}, 1: {"two", true}, 2: {"three", true}, 3: {"", false}}
	for line, one := range cases {
		got, known := source.lineAt(line)
		if got != one.want || known != one.known {
			t.Errorf("lineAt(%d) = %q,%t, want %q,%t", line, got, known, one.want, one.known)
		}
	}
}

// Going over the bound must evict, not clear: whatever survives has to still
// be the right text, and whatever was dropped has to come back correctly.
func TestFileCacheEvictsAndStaysCorrect(t *testing.T) {
	dir := t.TempDir()
	client := encodingClient(t, protocol.PositionEncodingKindUTF16)
	paths := make([]string, 0, maxCachedFiles+5)
	for i := range maxCachedFiles + 5 {
		path := filepath.Join(dir, fmt.Sprintf("f%d.go", i))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("line of file %d\n", i)), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		paths = append(paths, path)
		client.fileText(path)
	}
	if len(client.files) > maxCachedFiles {
		t.Fatalf("expected at most %d entries, got %d", maxCachedFiles, len(client.files))
	}
	if len(client.files) < maxCachedFiles {
		t.Fatalf("expected eviction to keep the cache full, got %d entries", len(client.files))
	}
	for i, path := range paths {
		got, ok := client.fileText(path)
		want := fmt.Sprintf("line of file %d\n", i)
		if !ok || got.text != want {
			t.Fatalf("file %d: got %q (ok %t), want %q", i, got.text, ok, want)
		}
	}
}

// A server that says nothing about encoding gets UTF-16, the spec's default —
// assuming UTF-8 there is exactly the bug this fixes.
func TestNegotiatedEncodingDefaultsToUTF16(t *testing.T) {
	if got := negotiatedEncoding(nil); got != protocol.PositionEncodingKindUTF16 {
		t.Fatalf("expected utf-16 for a nil result, got %q", got)
	}
	if got := negotiatedEncoding(&protocol.InitializeResult{}); got != protocol.PositionEncodingKindUTF16 {
		t.Fatalf("expected utf-16 when the server is silent, got %q", got)
	}
	result := &protocol.InitializeResult{
		Capabilities: protocol.ServerCapabilities{PositionEncoding: protocol.PositionEncodingKindUTF8},
	}
	if got := negotiatedEncoding(result); got != protocol.PositionEncodingKindUTF8 {
		t.Fatalf("expected the server's choice to win, got %q", got)
	}
}

func TestOfferedEncodingsPreferUTF8(t *testing.T) {
	offered := offeredEncodings()
	if len(offered) == 0 || offered[0] != protocol.PositionEncodingKindUTF8 {
		t.Fatalf("expected utf-8 offered first, got %v", offered)
	}
}
