package sessionstore

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSnippetUTF8ByteWindow(t *testing.T) {
	for _, tc := range []struct{ name, text, token, want string }{
		{"cutoff", "a" + strings.Repeat("€", 100), "€", "a" + strings.Repeat("€", 79)},
		{"no match", "a" + strings.Repeat("€", 100), "missing", "a" + strings.Repeat("€", 79)},
		{"lower width", strings.Repeat("İK", 150) + "MARKER" + strings.Repeat("z", 200), "marker", strings.Repeat("İK", 24) + "MARKER" + strings.Repeat("z", 114)},
		{"start boundary", strings.Repeat("€", 101) + "MARKER" + strings.Repeat("z", 200), "marker", strings.Repeat("€", 40) + "MARKER" + strings.Repeat("z", 114)},
		{"end match", strings.Repeat("🙂", 100) + "END", "end", strings.Repeat("🙂", 30) + "END"},
		{"empty token", strings.Repeat("🙂", 100), "", strings.Repeat("🙂", 60)},
		{"exact", strings.Repeat("€", 80), "€", strings.Repeat("€", 80)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := snippet(tc.text, tc.token)
			if got != tc.want || !utf8.ValidString(got) || len(got) > 240 {
				t.Fatalf("snippet = %q (%d bytes), want %q", got, len(got), tc.want)
			}
		})
	}
}
