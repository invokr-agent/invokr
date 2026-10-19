package read_file

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBatchBudgetIncludesFirstFileAndFraming(t *testing.T) {
	wsp, root := testPolicy(t, false)
	for name, text := range map[string]string{
		"large.txt": strings.Repeat("x", multiReadBytes),
		"small.txt": "recoverable\n",
		"edge.txt":  strings.Repeat("y", multiReadBytes-multiReadNoticeBytes),
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, first := range []string{"large.txt", "edge.txt"} {
		got := runReadFiles(context.Background(), []string{first, "small.txt"}, wsp)
		if len(got) > multiReadBytes || strings.Contains(got, "=== "+first+" ===") {
			t.Fatalf("first file or header bypassed the cap: %d bytes", len(got))
		}
		if !strings.Contains(got, "=== small.txt ===\nrecoverable\n") || !strings.Contains(got, "another call): "+first) {
			t.Fatalf("later file or omitted filename lost: %s", got)
		}
		page, err := runReadFile(context.Background(), first, readArgs{Offset: 1, Limit: 1}, wsp)
		if err != nil || page == "" {
			t.Fatalf("omitted file is not recoverable through single-file paging: %v", err)
		}
	}
}

func TestBatchBudgetBoundsOmissionNotice(t *testing.T) {
	wsp, _ := testPolicy(t, false)
	paths := make([]string, 100)
	for i := range paths {
		paths[i] = strings.Repeat("long-missing-name", 100)
	}
	got := runReadFiles(context.Background(), paths, wsp)
	if len(got) > multiReadBytes || !strings.Contains(got, "requested paths exceeded the batch budget") || !strings.Contains(got, "original paths list") {
		t.Fatalf("unbounded notice or lost recovery guidance: %d bytes: %.300s", len(got), got)
	}
}

func TestBatchBudgetExactEntryBoundary(t *testing.T) {
	wsp, root := testPolicy(t, false)
	const name = "edge.txt"
	framing := len("=== " + name + " ===\n\n")
	text := strings.Repeat("z", multiReadBytes-multiReadNoticeBytes-framing)
	if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	got := runReadFiles(context.Background(), []string{name, "missing.txt"}, wsp)
	if len(got) > multiReadBytes || !strings.Contains(got, "=== edge.txt ===\n"+text) || !strings.Contains(got, "another call): missing.txt") {
		t.Fatalf("boundary accounting changed: %d bytes", len(got))
	}
}
