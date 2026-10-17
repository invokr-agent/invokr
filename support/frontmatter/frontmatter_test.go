package frontmatter

import "testing"

func TestSplitFrontMatterNoFrontMatter(t *testing.T) {
	rawMeta, body, err := SplitFrontMatter([]byte("Just a body.\n"))
	if err != nil {
		t.Fatalf("SplitFrontMatter() error = %v", err)
	}
	if rawMeta != nil {
		t.Fatalf("expected nil rawMeta, got %q", rawMeta)
	}
	if body != "Just a body.\n" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestSplitFrontMatterStripsBOM(t *testing.T) {
	data := append([]byte("\xef\xbb\xbf"), []byte("---\nname: x\n---\nBody.\n")...)
	rawMeta, body, err := SplitFrontMatter(data)
	if err != nil {
		t.Fatalf("SplitFrontMatter() error = %v", err)
	}
	if string(rawMeta) != "name: x" {
		t.Fatalf("unexpected rawMeta: %q", rawMeta)
	}
	if body != "Body.\n" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestSplitFrontMatterUnterminated(t *testing.T) {
	_, _, err := SplitFrontMatter([]byte("---\nname: x\n"))
	if err == nil {
		t.Fatal("expected unterminated front matter error")
	}
}

func TestSplitFrontMatterTrimsCRLFBody(t *testing.T) {
	rawMeta, body, err := SplitFrontMatter([]byte("---\r\nname: x\r\n---\r\nBody.\r\n"))
	if err != nil {
		t.Fatalf("SplitFrontMatter() error = %v", err)
	}
	if string(rawMeta) != "name: x\r" {
		t.Fatalf("unexpected rawMeta: %q", rawMeta)
	}
	if body != "Body.\r\n" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestSplitFrontMatterTrimsLFBody(t *testing.T) {
	rawMeta, body, err := SplitFrontMatter([]byte("---\nname: x\n---\nBody.\n"))
	if err != nil {
		t.Fatalf("SplitFrontMatter() error = %v", err)
	}
	if string(rawMeta) != "name: x" {
		t.Fatalf("unexpected rawMeta: %q", rawMeta)
	}
	if body != "Body.\n" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestSplitFrontMatterAllowsOpeningWhitespace(t *testing.T) {
	rawMeta, _, err := SplitFrontMatter([]byte("--- \t\nname: x\n---\n\nBody.\n"))
	if err != nil {
		t.Fatalf("SplitFrontMatter() error = %v", err)
	}
	if string(rawMeta) != "name: x" {
		t.Fatalf("unexpected rawMeta: %q", rawMeta)
	}
}
