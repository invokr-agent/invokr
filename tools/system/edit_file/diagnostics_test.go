package edit_file

import (
	"strings"
	"testing"
)

func TestNotFoundPrefersMarkdownOverCSVPrefix(t *testing.T) {
	lines := make([]string, 114)
	lines[82] = "\tif lines[2] != `2,\"say \"\"hi\"\"\"` {"
	lines[113] = "\tif lines[2] != \"| 1 | a | 2026-09-08 | work |  |\" {"
	old := "\tif lines[2] != `| 1 | a | 2026-09-08 | work |  |` {"
	_, _, err := applyOriginalOps(strings.Join(lines, "\n"), editArgs{Edits: []editOp{{Old: old, New: "replacement"}}}, "table_test.go", "\n")
	if err == nil || !strings.Contains(err.Error(), "starts at line 114") {
		t.Fatalf("expected Markdown candidate at line 114: %v", err)
	}
	if strings.Contains(err.Error(), lines[82]) {
		t.Fatalf("unrelated CSV candidate quoted: %v", err)
	}
}

func TestNotFoundDifferencePrecedesContext(t *testing.T) {
	err := notFoundError("before\nvalue = \"hello\"\nafter\n", "value = `hello`")
	got := err.Error()
	difference, context := strings.Index(got, "first difference"), strings.Index(got, "\nbefore\n")
	if difference < 0 || context < 0 || difference > context {
		t.Fatalf("difference must precede copyable context: %s", got)
	}
}

func TestBatchRejectsOverlappingOriginalAnchors(t *testing.T) {
	for _, old := range []string{"alpha = 1", "alpha  = 1"} {
		t.Run(old, func(t *testing.T) {
			_, _, err := applyOriginalOps("alpha = 1\nbeta\n", editArgs{Edits: []editOp{
				{Old: "alpha = 1", New: "ALPHA = 2"},
				{Old: old, New: "other"},
			}}, "f.go", "\n")
			if err == nil {
				t.Fatal("expected invalidated anchor error")
			}
			for _, want := range []string{"overlapping edits", "No changes were written", "merge"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("missing %q: %v", want, err)
				}
			}
		})
	}
}
