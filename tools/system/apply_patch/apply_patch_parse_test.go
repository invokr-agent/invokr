package apply_patch_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/tools/core"
)

func applyPatchText(t *testing.T, root string, patch string) error {
	t.Helper()
	args, err := json.Marshal(map[string]string{"patch": patch})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	caller := newApplyPatchTestCaller(t, root)
	_, callErr := caller.HandleCall(context.Background(), &core.ToolCall{Name: "apply_patch", Arguments: args})
	return callErr
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("expected %q, got %q", want, string(got))
	}
}

func assertApplyPatchError(t *testing.T, root string, patch string, want string) {
	t.Helper()
	err := applyPatchText(t, root, patch)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error containing %q, got %v", want, err)
	}
}

// An Add File body with no "+" line at all is the whole new file, verbatim:
// the prefix is ceremony for an add, and small models routinely omit it.
func TestApplyPatchAddFileAcceptsUnprefixedBody(t *testing.T) {
	root := t.TempDir()
	if err := applyPatchText(t, root, "*** Begin Patch\n*** Add File: new.txt\nnope\nmore\n*** End Patch"); err != nil {
		t.Fatalf("unprefixed add body: %v", err)
	}
	assertFileContent(t, filepath.Join(root, "new.txt"), "nope\nmore")
}

// A body that mixes prefixed and unprefixed lines is what a small model
// actually emits — prefixing the first lines, then forgetting. Each line
// contributes its content with one leading "+" removed if present.
func TestApplyPatchAddFileAcceptsMixedBody(t *testing.T) {
	root := t.TempDir()
	if err := applyPatchText(t, root, "*** Begin Patch\n*** Add File: new.txt\n+ok\nnope\n*** End Patch"); err != nil {
		t.Fatalf("mixed add body: %v", err)
	}
	assertFileContent(t, filepath.Join(root, "new.txt"), "ok\nnope")
}

// Add File over an existing path stays an error: overwriting is destructive,
// and the benchmark reruns showed no benefit from allowing it. The message
// names the section that does work.
func TestApplyPatchRejectsAddFileOverExistingFile(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), "old\n")
	assertApplyPatchError(t, root,
		"*** Begin Patch\n*** Add File: a.txt\n+brand new\n*** End Patch",
		"use *** Update File: a.txt")
	assertFileContent(t, filepath.Join(root, "a.txt"), "old\n")
}

// A patch whose sections all parsed but that was cut off before the
// terminator still applies; one that parsed nothing does not.
func TestApplyPatchToleratesMissingEndMarker(t *testing.T) {
	root := t.TempDir()
	if err := applyPatchText(t, root, "*** Begin Patch\n*** Add File: new.txt\n+hello"); err != nil {
		t.Fatalf("truncated patch: %v", err)
	}
	assertFileContent(t, filepath.Join(root, "new.txt"), "hello")
	assertApplyPatchError(t, t.TempDir(), "*** Begin Patch\ngarbage", "unexpected patch line")
}

// Trailing junk on the terminator comes from the model's own JSON encoding.
func TestApplyPatchToleratesTrailingJunkOnEndMarker(t *testing.T) {
	root := t.TempDir()
	if err := applyPatchText(t, root, "*** Begin Patch\n*** Add File: new.txt\n+hello\n*** End Patch\""); err != nil {
		t.Fatalf("junk terminator: %v", err)
	}
	assertFileContent(t, filepath.Join(root, "new.txt"), "hello")
}

// The @@ header carries nothing this parser uses — hunks are located by
// context match — so a body line where it should be starts an implicit hunk.
func TestApplyPatchUpdateFileAcceptsImplicitHunk(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), "one\ntwo\n")
	if err := applyPatchText(t, root, "*** Begin Patch\n*** Update File: a.txt\n-one\n+ONE\n*** End Patch"); err != nil {
		t.Fatalf("implicit hunk: %v", err)
	}
	assertFileContent(t, filepath.Join(root, "a.txt"), "ONE\ntwo\n")
}

// An unprefixed line in an update hunk is context — models routinely drop the
// leading-space marker, and the context match against the real file is the
// safety net, not the parser.
func TestApplyPatchUpdateFileAcceptsUnprefixedContextLine(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), "class Thing:\n    x = 1\n")
	if err := applyPatchText(t, root, "*** Begin Patch\n*** Update File: a.txt\nclass Thing:\n+    y = 2\n*** End Patch"); err != nil {
		t.Fatalf("unprefixed context: %v", err)
	}
	assertFileContent(t, filepath.Join(root, "a.txt"), "class Thing:\n    y = 2\n    x = 1\n")
}

func TestApplyPatchRejectsUpdateFileWithNoHunks(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), "one\n")
	assertApplyPatchError(t, root,
		"*** Begin Patch\n*** Update File: a.txt\n*** End Patch",
		`update file "a.txt" has no hunks`)
}

// A line with an unknown marker is context taken verbatim; when it matches
// nothing in the file the context match reports it, not the parser.
func TestApplyPatchUnknownMarkerLineFailsContextMatch(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), "one\n")
	assertApplyPatchError(t, root,
		"*** Begin Patch\n*** Update File: a.txt\n@@\n?one\n*** End Patch",
		`context not found`)
}

// Delete + Add of the same path in one patch is the rewrite idiom models use;
// the delete makes the path absent for the add. Adding without the delete
// still fails with "file already exists".
func TestApplyPatchDeleteThenAddSameFileRewritesIt(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), "old\n")
	patch := "*** Begin Patch\n*** Delete File: a.txt\n*** Add File: a.txt\n+new\n*** End Patch"
	if err := applyPatchText(t, root, patch); err != nil {
		t.Fatalf("delete+add: %v", err)
	}
	assertFileContent(t, filepath.Join(root, "a.txt"), "new")
}

// A patch that opens with hunks and no file header names the missing header
// instead of the generic parse error (observed 4x in bench runs: the model
// means "the file I was editing", which cannot be guessed safely).
func TestApplyPatchHunkBeforeFileHeaderNamesMissingHeader(t *testing.T) {
	assertApplyPatchError(t, t.TempDir(),
		"*** Begin Patch\n@@\n def add(self, other):\n+    pass\n*** End Patch",
		"hunk found before any file header")
}

func TestApplyPatchRejectsContentAfterEndMarker(t *testing.T) {
	root := t.TempDir()
	assertApplyPatchError(t, root,
		"*** Begin Patch\n*** Add File: new.txt\n+hello\n*** End Patch\ntrailing",
		"unexpected content after "+constants.ApplyPatchEndMarker)
}

func TestApplyPatchRejectsOversizedPatch(t *testing.T) {
	body := strings.Repeat("+x\n", constants.ApplyPatchMaxBytes/3+1)
	assertApplyPatchError(t, t.TempDir(),
		"*** Begin Patch\n*** Add File: big.txt\n"+body+"*** End Patch",
		"patch exceeds max_patch_bytes")
}

func TestApplyPatchRejectsAmbiguousContextEndToEnd(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "dup.txt")
	original := "dup\nmiddle\ndup\n"
	mustWriteFile(t, path, original)

	assertApplyPatchError(t, root,
		"*** Begin Patch\n*** Update File: dup.txt\n@@\n dup\n+added\n*** End Patch",
		"context matches 2 locations")

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(content) != original {
		t.Fatalf("file was modified: %q", string(content))
	}
}
