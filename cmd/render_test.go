package cmd

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/capability/jobs"
	"github.com/invokr-ai/invokr/capability/sessionstore"
	"github.com/invokr-ai/invokr/surface/ui"

	"charm.land/lipgloss/v2"
)

// The whole point of routing through a Painter: anything that is not a
// terminal gets the same words with no escapes, so `invokr sessions | grep`
// and a redirected log stay readable.
func TestRenderedOutputCarriesNoEscapesOffTerminal(t *testing.T) {
	var out bytes.Buffer
	status := "ok"
	if err := renderSessionTable(&out, []sessionstore.SessionInfo{
		{ID: "abc", Title: "fix the build", Messages: 4, UpdatedAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	if err := renderJobTable(&out, []jobs.Job{
		{Name: "nightly", Schedule: "0 3 * * *", ApprovalPolicy: "allow", LastStatus: &status},
	}); err != nil {
		t.Fatal(err)
	}
	if err := renderChecks(&out, []checkResult{ok("provider", "reachable")}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatalf("escape sequence written to a non-terminal:\n%q", out.String())
	}
}

func TestSessionTableCarriesEveryColumn(t *testing.T) {
	var out bytes.Buffer
	if err := renderSessionTable(&out, []sessionstore.SessionInfo{
		{ID: "session-1", Title: "fix the build", Messages: 4, UpdatedAt: time.Now().Add(-time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "UPDATED", "MSGS", "TITLE", "session-1", "fix the build", "4", "ago"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("listing is missing %q:\n%s", want, out.String())
		}
	}
}

func TestEmptyListingsExplainThemselves(t *testing.T) {
	var sessions, joblist bytes.Buffer
	if err := renderSessionTable(&sessions, nil); err != nil {
		t.Fatal(err)
	}
	if err := renderJobTable(&joblist, nil); err != nil {
		t.Fatal(err)
	}
	// An empty table is indistinguishable from a broken command; both empty
	// states say what to do instead.
	if !strings.Contains(sessions.String(), "invokr") || !strings.Contains(joblist.String(), "invokr") {
		t.Fatalf("empty listings should point somewhere:\n%s%s", sessions.String(), joblist.String())
	}
}

// spyRenderer paints with the style's own foreground color as a prefix, so a
// test can assert which palette entry a value was rendered with without
// decoding escape sequences.
func spyRenderer() renderer {
	return renderer{
		paint: func(style lipgloss.Style, text string) string {
			return fmt.Sprint(style.GetForeground()) + "|" + text
		},
		theme: ui.Invokr,
	}
}

func painted(style lipgloss.Style, text string) string {
	return fmt.Sprint(style.GetForeground()) + "|" + text
}

// "allow" is graded as a good outcome by the generic status vocabulary; on a
// scheduled job it means every tool call is approved with nobody watching.
func TestApprovalPolicyIsPaintedAsRisk(t *testing.T) {
	r := spyRenderer()
	if got, want := r.policy("allow"), painted(ui.Invokr.Danger, "allow"); got != want {
		t.Fatalf("policy(allow) = %q, want %q", got, want)
	}
	if got, want := r.policy("deny"), painted(ui.Invokr.Muted, "deny"); got != want {
		t.Fatalf("policy(deny) = %q, want %q", got, want)
	}
}

func TestDoctorStatusPaintsBySeverity(t *testing.T) {
	r := spyRenderer()
	for status, style := range map[string]lipgloss.Style{
		"ok": ui.Invokr.Success, "fail": ui.Invokr.Danger, "warn": ui.Invokr.Warn,
	} {
		got := doctorStatus(r, status)
		if want := painted(style, fmt.Sprintf("%-4s", strings.ToUpper(status))); got != want {
			t.Fatalf("doctorStatus(%q) = %q, want %q", status, got, want)
		}
	}
}
