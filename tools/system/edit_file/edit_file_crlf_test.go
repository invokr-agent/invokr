package edit_file

import (
	"testing"

	"github.com/invokr-ai/invokr/tools/system/workspace"
)

func TestReplaceInLineEndings(t *testing.T) {
	cases := []struct {
		name               string
		content            string
		old, replace, want string
	}{
		{
			name:    "lf old against crlf file stays crlf",
			content: "a\r\nold one\r\nold two\r\nb\r\n",
			old:     "old one\nold two\n",
			replace: "new one\nnew two\n",
			want:    "a\r\nnew one\r\nnew two\r\nb\r\n",
		},
		{
			name:    "lf old single line against crlf file stays crlf",
			content: "a\r\nold\r\nb\r\n",
			old:     "old",
			replace: "new",
			want:    "a\r\nnew\r\nb\r\n",
		},
		{
			name:    "crlf new against crlf file is not doubled",
			content: "a\r\nold\r\nb\r\n",
			old:     "old\r\n",
			replace: "one\r\ntwo\r\n",
			want:    "a\r\none\r\ntwo\r\nb\r\n",
		},
		{
			name:    "crlf old against lf file yields lf",
			content: "a\nold one\nold two\nb\n",
			old:     "old one\r\nold two\r\n",
			replace: "new one\r\nnew two\r\n",
			want:    "a\nnew one\nnew two\nb\n",
		},
		{
			name:    "mixed file with crlf majority normalizes the edited region",
			content: "a\r\nold one\r\nold two\r\nb\nc\r\n",
			old:     "old one\nold two\n",
			replace: "new one\nnew two",
			want:    "a\r\nnew one\r\nnew two\r\nb\nc\r\n",
		},
		{
			// The model that quotes "old" byte-faithfully is the one that then
			// hand-writes "new" with bare newlines: the exact rung matches
			// byte-for-byte, but the replacement still takes the file's ending.
			name:    "exact byte match still writes the file's ending",
			content: "a\r\nold\r\nb\r\n",
			old:     "old\r\nb",
			replace: "x\ny",
			want:    "a\r\nx\r\ny\r\n",
		},
		{
			name:    "exact byte match in an lf file stays lf",
			content: "a\nold\nb\n",
			old:     "old\nb",
			replace: "x\ny",
			want:    "a\nx\ny\n",
		},
		{
			name:    "crlf file without trailing newline",
			content: "a\r\nold",
			old:     "old",
			replace: "new",
			want:    "a\r\nnew",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n, err := replaceIn(tc.content, tc.old, tc.replace, false, workspace.DominantEOL(tc.content))
			if err != nil {
				t.Fatalf("replaceIn: %v", err)
			}
			if n != 1 {
				t.Fatalf("replaced = %d, want 1", n)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReplaceInCRLFAmbiguityStillErrors(t *testing.T) {
	if _, _, err := replaceIn("x\r\nx\r\n", "x", "y", false, "\r\n"); err == nil {
		t.Fatal("multiple matches must error without replace_all")
	}
}

func TestReplaceAllKeepsCRLF(t *testing.T) {
	got, n, err := replaceIn("x\r\nx\r\n", "x", "y", true, "\r\n")
	if err != nil {
		t.Fatalf("replaceIn: %v", err)
	}
	if n != 2 || got != "y\r\ny\r\n" {
		t.Fatalf("got %q (n=%d)", got, n)
	}
}

func TestEditFileKeepsCRLFEndToEnd(t *testing.T) {
	wsp, root := testPolicy(t)
	write(t, root, "w.txt", "alpha\r\nbeta\r\ngamma\r\n")
	p, err := plan(t, wsp, editArgs{Path: "w.txt", Edits: []editOp{{Old: "beta\n", New: "one\ntwo\n"}}})
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	if p.updated != "alpha\r\none\r\ntwo\r\ngamma\r\n" {
		t.Fatalf("updated = %q", p.updated)
	}
}

func TestCreateNewFileKeepsBytesVerbatim(t *testing.T) {
	wsp, _ := testPolicy(t)
	p, err := plan(t, wsp, editArgs{Path: "fresh.txt", Edits: []editOp{{New: "a\r\nb\n"}}})
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	if !p.created || p.updated != "a\r\nb\n" {
		t.Fatalf("created=%v updated=%q", p.created, p.updated)
	}
}
