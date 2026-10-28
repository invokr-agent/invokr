package jobs_test

import (
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/jobs"
)

func TestMarkerComment(t *testing.T) {
	got := jobs.MarkerComment("abc-123")
	want := "# invokr-job:abc-123"
	if got != want {
		t.Errorf("MarkerComment: got %q, want %q", got, want)
	}
}

func TestAddCrontabEntry_EmptyContent(t *testing.T) {
	got, err := jobs.AddCrontabEntry("", "*/5 * * * *", "/bin/invokr", "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "# invokr-job:x\n*/5 * * * * '/bin/invokr' jobs run x\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAddCrontabEntry_ContentWithTrailingNewline(t *testing.T) {
	existing := "0 * * * * /bin/other\n"
	got, err := jobs.AddCrontabEntry(existing, "*/5 * * * *", "/bin/invokr", "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, existing) {
		t.Errorf("existing content not preserved; got %q", got)
	}
	if !strings.Contains(got, "# invokr-job:x\n*/5 * * * * '/bin/invokr' jobs run x\n") {
		t.Errorf("block not appended; got %q", got)
	}
}

func TestAddCrontabEntry_QuotesBinaryPath(t *testing.T) {
	got, err := jobs.AddCrontabEntry("", "*/5 * * * *", "/Application Support/Invokr's/bin", "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "# invokr-job:x\n*/5 * * * * '/Application Support/Invokr'\"'\"'s/bin' jobs run x\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAddCrontabEntry_ContentWithoutTrailingNewline(t *testing.T) {
	existing := "0 * * * * /bin/other"
	got, err := jobs.AddCrontabEntry(existing, "*/5 * * * *", "/bin/invokr", "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, existing+"\n") {
		t.Errorf("expected newline inserted; got %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("output should end with newline; got %q", got)
	}
}

func TestAddCrontabEntry_Idempotent(t *testing.T) {
	first, err := jobs.AddCrontabEntry("", "*/5 * * * *", "/bin/invokr", "x")
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	second, err := jobs.AddCrontabEntry(first, "*/5 * * * *", "/bin/invokr", "x")
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if second != first {
		t.Errorf("idempotency broken:\nfirst:  %q\nsecond: %q", first, second)
	}
}

func TestAddCrontabEntry_EmptyFields(t *testing.T) {
	cases := []struct {
		name     string
		schedule string
		binary   string
		id       string
	}{
		{"empty schedule", "", "/bin/invokr", "x"},
		{"empty binary", "*/5 * * * *", "", "x"},
		{"empty id", "*/5 * * * *", "/bin/invokr", ""},
		{"whitespace schedule", "   ", "/bin/invokr", "x"},
		{"whitespace binary", "*/5 * * * *", "   ", "x"},
		{"whitespace id", "*/5 * * * *", "/bin/invokr", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := jobs.AddCrontabEntry("", tc.schedule, tc.binary, tc.id)
			if err == nil {
				t.Errorf("expected error for %q/%q/%q, got nil", tc.schedule, tc.binary, tc.id)
			}
		})
	}
}

func TestStripCrontabEntries_RemovesBlock(t *testing.T) {
	content := "before\n# invokr-job:x\n*/5 * * * * /bin/invokr jobs run x\nafter\n"
	got := jobs.StripCrontabEntries(content, "x")
	want := "before\nafter\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripCrontabEntries_NoMatch(t *testing.T) {
	content := "0 * * * * /bin/other\n"
	got := jobs.StripCrontabEntries(content, "x")
	if got != content {
		t.Errorf("got %q, want %q", got, content)
	}
}

func TestStripCrontabEntries_MarkerAsLastLine(t *testing.T) {
	// Marker is the last line (no following line) — must not panic.
	content := "before\n# invokr-job:x\n"
	got := jobs.StripCrontabEntries(content, "x")
	want := "before\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripCrontabEntries_TwoMarkers_StripOne(t *testing.T) {
	content := "# invokr-job:a\n0 * * * * /bin/m jobs run a\n# invokr-job:b\n1 * * * * /bin/m jobs run b\n"
	got := jobs.StripCrontabEntries(content, "a")
	want := "# invokr-job:b\n1 * * * * /bin/m jobs run b\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripAllInvokrEntries_RemovesBoth(t *testing.T) {
	content := "before\n# invokr-job:a\nline-a\n# invokr-job:b\nline-b\nafter\n"
	got := jobs.StripAllInvokrEntries(content)
	want := "before\nafter\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripAllInvokrEntries_NoMarkers(t *testing.T) {
	content := "0 * * * * /bin/other\n"
	got := jobs.StripAllInvokrEntries(content)
	if got != content {
		t.Errorf("got %q, want %q", got, content)
	}
}

func TestStripDoubleIdempotent(t *testing.T) {
	content := "# invokr-job:x\n*/5 * * * * /bin/invokr jobs run x\n"
	first := jobs.StripCrontabEntries(content, "x")
	second := jobs.StripCrontabEntries(first, "x")
	if first != second {
		t.Errorf("double strip not idempotent: first=%q second=%q", first, second)
	}
}
