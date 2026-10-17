package workspace

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	scannerBufferBytes   = 64 * 1024
	scannerMaxTokenBytes = 5 << 20
)

// NewFileScanner returns a line scanner sized for workspace files: a 64 KiB
// initial buffer growing to a 5 MiB max token.
func NewFileScanner(file *os.File) *bufio.Scanner {
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, scannerBufferBytes), scannerMaxTokenBytes)
	return scanner
}

// ObviousBinaryExtension reports whether the path's extension marks a file
// that is never worth scanning as text.
func ObviousBinaryExtension(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".7z", ".a", ".avi", ".bin", ".bmp", ".class", ".db", ".dll", ".dylib", ".exe", ".gif", ".gz", ".ico", ".jar", ".jpeg", ".jpg", ".mov", ".mp3", ".mp4", ".o", ".pdf", ".png", ".pyc", ".so", ".sqlite", ".tar", ".webp", ".woff", ".woff2", ".zip":
		return true
	default:
		return false
	}
}

// LooksBinary reports whether a scanned line carries binary content an
// extension check would miss: a NUL byte or invalid UTF-8.
func LooksBinary(line string) bool { return strings.Contains(line, "\x00") || !utf8.ValidString(line) }

// DisplayPath returns abs relative to the allowed root containing it, in
// slash form, or abs unchanged when no root contains it. It is the path shape
// AllowsHiddenPath expects: hidden components of the root itself must not
// count against a file inside it.
func (p Policy) DisplayPath(abs string) string {
	return relInsideAny(p.roots, abs, filepath.ToSlash(abs))
}

// LineWindow is one paged read of a text file: lines Start..End of Total,
// joined verbatim in Text. Truncated marks a partial read, whether by line
// limit or byte cap.
type LineWindow struct {
	Text              string
	Start, End, Total int
	Truncated         bool
}

// Marker returns "" for a complete read, otherwise a single trailing line
// telling the model how to continue. A truncated window always ends with the
// last line's own terminator, so the marker needs no leading newline.
func (w LineWindow) Marker() string {
	if !w.Truncated {
		return ""
	}
	return fmt.Sprintf("[read_file: lines %d-%d of %d (truncated); call again with {\"offset\":%d}]", w.Start, w.End, w.Total, w.End+1)
}

// readLine reads one line including its terminator, capped at the scanner max
// token size so a pathological single-line file cannot exhaust memory.
func readLine(reader *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		chunk, err := reader.ReadSlice('\n')
		b.Write(chunk)
		if b.Len() > scannerMaxTokenBytes {
			return "", fmt.Errorf("line longer than %d bytes", scannerMaxTokenBytes)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return b.String(), err
	}
}

// scanLines feeds every line (terminator included) to keep and returns the
// total line count. readLine only returns "" at EOF, so every non-empty
// return is exactly one line.
func scanLines(reader *bufio.Reader, keep func(lineNo int, line string) error) (int, error) {
	lineNo := 0
	for {
		line, rerr := readLine(reader)
		if rerr != nil && rerr != io.EOF {
			return lineNo, rerr
		}
		if line != "" {
			lineNo++
			if err := keep(lineNo, line); err != nil {
				return lineNo, err
			}
		}
		if rerr == io.EOF {
			return lineNo, nil
		}
	}
}

// ReadLineWindow reads up to limit lines starting at 1-based offset, stopping
// early once the window holds maxBytes, and keeps counting past the window so
// Total is exact. Lines keep their terminators verbatim, so a complete read is
// byte-identical to the file. A binary-looking line inside the window is an
// error.
func ReadLineWindow(path string, offset, limit, maxBytes int) (LineWindow, error) {
	file, err := os.Open(path)
	if err != nil {
		return LineWindow{}, err
	}
	defer func() { _ = file.Close() }()

	window := LineWindow{Start: offset}
	var text strings.Builder
	kept := 0
	total, err := scanLines(bufio.NewReaderSize(file, scannerBufferBytes), func(lineNo int, line string) error {
		if lineNo < offset || kept >= limit || (maxBytes > 0 && text.Len() >= maxBytes) {
			return nil
		}
		if LooksBinary(line) {
			return fmt.Errorf("file %q looks binary at line %d; read_file returns text only", path, lineNo)
		}
		text.WriteString(line)
		kept++
		window.End = lineNo
		return nil
	})
	if err != nil {
		return LineWindow{}, err
	}
	if offset > 1 && offset > total {
		return LineWindow{}, fmt.Errorf("offset %d is past the end of the file (%d lines)", offset, total)
	}
	window.Total = total
	window.Text = text.String()
	window.Truncated = window.End < total
	if kept == 0 {
		window.End = window.Start - 1
	}
	return window, nil
}
