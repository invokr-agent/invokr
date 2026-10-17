// Package frontmatter splits a markdown document into raw YAML front matter
// and body text.
package frontmatter

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// SplitFrontMatter splits a markdown document into its raw YAML front matter
// (nil when absent) and body. Callers unmarshal the YAML into their own
// metadata struct.
func SplitFrontMatter(data []byte) (rawMeta []byte, body string, err error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !bytes.HasPrefix(data, []byte("---")) {
		return nil, string(data), nil
	}
	before, after, ok := bytes.Cut(data, []byte{'\n'})
	if !ok || !bytes.Equal(bytes.TrimRight(before, " \t\r"), []byte("---")) {
		return nil, string(data), nil
	}
	rest := after
	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 {
		return nil, "", fmt.Errorf("unterminated front matter")
	}
	raw, bodyBytes := rest[:end], rest[end+len("\n---"):]
	bodyBytes = bytes.TrimPrefix(bodyBytes, []byte("\r\n"))
	bodyBytes = bytes.TrimPrefix(bodyBytes, []byte("\n"))
	return raw, string(bodyBytes), nil
}

// Parse splits a markdown document and unmarshals its front matter into T.
// A document without front matter yields the zero T and no error.
func Parse[T any](data []byte) (T, string, error) {
	var meta T
	rawMeta, body, err := SplitFrontMatter(data)
	if err != nil {
		return meta, "", err
	}
	if rawMeta == nil {
		return meta, body, nil
	}
	if err := yaml.Unmarshal(rawMeta, &meta); err != nil {
		var zero T
		return zero, "", fmt.Errorf("parse frontmatter: %w", err)
	}
	return meta, body, nil
}
