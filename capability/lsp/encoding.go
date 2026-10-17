package lsp

import (
	"os"
	"strings"
	"unicode/utf8"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// Positions on the wire are 0-based offsets counted in code units of the
// negotiated encoding — UTF-16 unless the server agrees to something else. The
// positions this package hands out are 1-based UTF-8 byte columns, which is
// what Go tooling prints and what a caller can hand straight back.
//
// On an ASCII line the two agree, which is why a bare +1 looked correct. On a
// line with any non-ASCII before the symbol it is not, and a wrong column is
// worse than a missing one: the next definition call lands where the symbol is
// not and comes back empty, which reads as "no such symbol".

// maxCachedFiles bounds the line cache. A references query across a large
// workspace can name hundreds of files; this is a conversion aid, not a store.
const maxCachedFiles = 64

type fileStamp struct{ size, mod int64 }

type cachedFile struct {
	stamp fileStamp
	sourceText
	// used is the tick at which this entry was last served, so a full cache can
	// drop the least recently used entry instead of every entry.
	used uint64
}

// sourceText is a copy of a file plus the byte offset every line starts at.
// Converting a run of results in one file — which is what a references query
// is — then costs one scan of the text rather than one scan per position.
type sourceText struct {
	text  string
	lines []int
}

func newSourceText(text string) sourceText {
	lines := []int{0}
	for i := range len(text) {
		if text[i] == '\n' {
			lines = append(lines, i+1)
		}
	}
	return sourceText{text: text, lines: lines}
}

// lineAt returns one 0-based line of text, without its terminator. It reports
// false when there is no such line, so a caller can tell "line unknown" from
// "line is empty". The zero sourceText knows no lines at all.
func (s sourceText) lineAt(line uint32) (string, bool) {
	if int(line) >= len(s.lines) {
		return "", false
	}
	start := s.lines[line]
	end := len(s.text)
	if int(line)+1 < len(s.lines) {
		end = s.lines[line+1] - 1
	}
	return strings.TrimSuffix(s.text[start:end], "\r"), true
}

// offeredEncodings lists what this client accepts, best first. UTF-8 comes
// first because it makes every conversion below the identity — but measured
// against real servers, both gopls and pyright answer utf-16, so conversion is
// the normal path and the UTF-8 branch is a fast path that rarely fires. Do not
// let that shape the code: the expensive branch is the one that runs.
func offeredEncodings() []protocol.PositionEncodingKind {
	return []protocol.PositionEncodingKind{protocol.PositionEncodingKindUTF8, protocol.PositionEncodingKindUTF16}
}

// negotiatedEncoding is what the server picked. Saying nothing means UTF-16 —
// the spec's default, and what every server predating the capability does.
func negotiatedEncoding(result *protocol.InitializeResult) protocol.PositionEncodingKind {
	if result == nil || result.Capabilities.PositionEncoding == "" {
		return protocol.PositionEncodingKindUTF16
	}
	return result.Capabilities.PositionEncoding
}

// units reports how many code units r occupies in the negotiated encoding.
// Only what offeredEncodings offers can be negotiated, so the two encodings
// there are the two handled here — a server naming anything else is treated as
// UTF-16, the spec's default.
func (c *Client) units(r rune) int {
	if c.encoding == protocol.PositionEncodingKindUTF8 {
		return utf8.RuneLen(r)
	}
	if r > 0xFFFF {
		return 2
	}
	return 1
}

// wireToByte converts a 0-based wire offset into a 0-based byte offset in line.
func (c *Client) wireToByte(line string, offset uint32) int {
	if c.encoding == protocol.PositionEncodingKindUTF8 {
		return int(offset)
	}
	seen := 0
	for i, r := range line {
		if seen >= int(offset) {
			return i
		}
		seen += c.units(r)
	}
	return len(line)
}

// byteToWire converts a 0-based byte offset in line into a 0-based wire offset.
func (c *Client) byteToWire(line string, offset int) uint32 {
	if c.encoding == protocol.PositionEncodingKindUTF8 {
		return uint32(offset)
	}
	seen := 0
	for i, r := range line {
		if i >= offset {
			break
		}
		seen += c.units(r)
	}
	return uint32(seen)
}

// column converts a wire position into the 1-based byte column callers see.
// A line the client cannot see — past the end of its copy, or a file it cannot
// read — falls back to the wire offset rather than converting against nothing:
// a missing conversion is honest, a conversion against an empty line is a
// plausible-looking column 1 that points at the wrong place.
func (c *Client) column(docURI uri.URI, position protocol.Position) int {
	if c.encoding == protocol.PositionEncodingKindUTF8 {
		return oneBased(position.Character)
	}
	text, known := c.lineFor(docURI, position.Line)
	if !known {
		return oneBased(position.Character)
	}
	return c.wireToByte(text, position.Character) + 1
}

// position builds the wire position for a 1-based line and byte column.
func (c *Client) position(docURI uri.URI, line, column int) protocol.Position {
	wireLine := zeroBased(line)
	if c.encoding == protocol.PositionEncodingKindUTF8 {
		return protocol.Position{Line: wireLine, Character: zeroBased(column)}
	}
	text, known := c.lineFor(docURI, wireLine)
	if !known {
		return protocol.Position{Line: wireLine, Character: zeroBased(column)}
	}
	return protocol.Position{Line: wireLine, Character: c.byteToWire(text, int(zeroBased(column)))}
}

// lineFor returns the text of a 0-based line, preferring the copy the server
// was given over what is on disk now: the wire position describes that copy.
// The second result is false when the line is unknown — which is not the same
// as the line being empty, and the caller must not treat it as such.
func (c *Client) lineFor(docURI uri.URI, line uint32) (string, bool) {
	c.mu.Lock()
	known, opened := c.opened[docURI]
	c.mu.Unlock()
	if opened {
		return known.lineAt(line)
	}
	source, ok := c.fileText(docURI.FsPath())
	if !ok {
		return "", false
	}
	return source.lineAt(line)
}

// fileText caches files the server named but this client never opened — mostly
// the standard library and the module cache, where definitions land. Keyed on
// the stat identity, so an edited file is re-read rather than mis-converted.
func (c *Client) fileText(path string) (sourceText, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return sourceText{}, false
	}
	stamp := fileStamp{size: info.Size(), mod: info.ModTime().UnixNano()}
	c.mu.Lock()
	cached, ok := c.files[path]
	if ok && cached.stamp == stamp {
		c.tick++
		cached.used = c.tick
		c.files[path] = cached
		c.mu.Unlock()
		return cached.sourceText, true
	}
	c.mu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return sourceText{}, false
	}
	source := newSourceText(string(data))
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.files) >= maxCachedFiles {
		c.evictLeastUsed()
	}
	c.tick++
	c.files[path] = cachedFile{stamp: stamp, sourceText: source, used: c.tick}
	return source, true
}

// evictLeastUsed drops one entry, the one served longest ago. Called with mu
// held. At this size a scan beats maintaining a list, and dropping one entry
// beats clearing the map: a references query naming more files than the cache
// holds would otherwise re-read every file for every result.
func (c *Client) evictLeastUsed() {
	oldest, since := "", uint64(0)
	for path, entry := range c.files {
		if since == 0 || entry.used < since {
			oldest, since = path, entry.used
		}
	}
	delete(c.files, oldest)
}
