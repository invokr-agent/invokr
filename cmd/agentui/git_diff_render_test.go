package agentui

import (
	"strings"
	"testing"
)

const sampleUnifiedDiff = `diff --git a/a.txt b/a.txt
index e69de29..4c5fd91 100644
--- a/a.txt
+++ b/a.txt
@@ -1,2 +1,3 @@
 one
-two
+2
+three
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
index d95f3ad..0000000
--- a/gone.txt
+++ /dev/null
@@ -1 +0,0 @@
-content
`

func TestParseUnifiedDiffSections(t *testing.T) {
	sections := parseUnifiedDiffSections(sampleUnifiedDiff)
	if len(sections) != 2 {
		t.Fatalf("sections = %d, want 2", len(sections))
	}
	if sections[0].path != "a.txt" {
		t.Fatalf("path = %q, want a.txt", sections[0].path)
	}
	if !strings.Contains(sections[0].before, "two") || strings.Contains(sections[0].after, "two") {
		t.Fatalf("before=%q after=%q, want 'two' removed", sections[0].before, sections[0].after)
	}
	if !strings.Contains(sections[0].after, "three") {
		t.Fatalf("after = %q, want 'three' added", sections[0].after)
	}
	if !sections[1].deleted {
		t.Fatal("want gone.txt marked deleted")
	}
}

func TestParseUnifiedDiffSectionsSkipsCommitHeader(t *testing.T) {
	show := "commit abc123\nAuthor: t\n\n    message\n\n" + sampleUnifiedDiff
	sections := parseUnifiedDiffSections(show)
	if len(sections) != 2 || sections[0].path != "a.txt" {
		t.Fatalf("sections = %#v, want header skipped", sections)
	}
}

func TestParseUnifiedDiffSectionsRejectsPlainText(t *testing.T) {
	if got := parseUnifiedDiffSections("just some text\nwith lines\n"); got != nil {
		t.Fatalf("sections = %#v, want nil for non-diff text", got)
	}
}

func TestGitReadPartsEnvelope(t *testing.T) {
	stat, diff, plain := gitReadParts(`{"stat":"1 file changed","diff":"diff --git a/x b/x"}`)
	if stat != "1 file changed" || !strings.HasPrefix(diff, "diff --git") || plain != "" {
		t.Fatalf("got %q %q %q", stat, diff, plain)
	}
	_, _, plain = gitReadParts(`{"output":"main\n* dev"}`)
	if plain != "main\n* dev" {
		t.Fatalf("plain = %q", plain)
	}
	_, _, plain = gitReadParts("not json")
	if plain != "not json" {
		t.Fatalf("plain = %q, want raw fallback", plain)
	}
}
