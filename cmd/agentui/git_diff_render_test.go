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

func TestRenderGitDiff(t *testing.T) {
	got := renderGitDiff("commit abc123\nAuthor: t\n\n"+sampleUnifiedDiff, 100, 0)
	if !strings.Contains(got, "a.txt") || !strings.Contains(got, "gone.txt") || !strings.Contains(got, "three") {
		t.Fatalf("git diff missing file or change: %q", got)
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

func TestGitReadPartsJSONFallback(t *testing.T) {
	for _, input := range []string{
		"```json\n{\"stat\":\"changed\",\"diff\":\"+new\"}\n```",
		`prefix {"stat":"changed","diff":"+new"} trailing {"diff":"wrong"}`,
	} {
		stat, diff, plain := gitReadParts(input)
		if stat != "changed" || diff != "+new" || plain != "" {
			t.Fatalf("gitReadParts(%q) = %q, %q, %q", input, stat, diff, plain)
		}
	}
	for _, input := range []string{
		`plain output`, `null`, `{}`, `prefix {"diff":"unfinished"`,
		`prefix {bad} {"diff":"must not skip first brace"}`,
		`prefix {"stat":"partial","diff":7}`,
		`{"stat":"strict","diff":7,"nested":{"diff":"must not unwrap valid JSON"}}`,
	} {
		stat, diff, plain := gitReadParts(input)
		if stat != "" || diff != "" || plain != input {
			t.Fatalf("invalid envelope %q must stay raw, got %q, %q, %q", input, stat, diff, plain)
		}
	}
}
