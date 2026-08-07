package agentprompt

import (
	"context"
	"testing"

	"github.com/invokr-ai/invokr/capability/skills"
	"github.com/invokr-ai/invokr/platform/events"
	toolcore "github.com/invokr-ai/invokr/tools/core"
)

// systemPrompts returns the recorded system-prompt payloads, which is what a
// bench run reads back to answer "was this tool advertised?" (test-bench #20).
func systemPrompts(rec *eventRecorder) []*events.SystemPromptPayload {
	var out []*events.SystemPromptPayload
	for _, ev := range rec.events {
		if ev.Type == events.EventSystemPrompt {
			out = append(out, ev.System)
		}
	}
	return out
}

func TestBuildRecordsSystemPromptOncePerSession(t *testing.T) {
	recorder := &eventRecorder{}
	builder, err := NewBuilder(Config{BasePrompt: "Base prompt.", PromptSource: "persona"}, testDeps(Deps{
		Events: recorder,
		Skills: &fakeSkills{specs: []skills.Spec{{Name: "tdd", Description: "Test first."}}},
		Tools:  &fakeTools{specs: []toolcore.ToolSpec{{Name: "shell", Description: "Run a command."}}},
	}))
	if err != nil {
		t.Fatalf("NewBuilder() error = %v", err)
	}

	first, err := builder.Build(context.Background(), Request{SessionID: "s1", UserMessage: "hi"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if _, err := builder.Build(context.Background(), Request{SessionID: "s1", UserMessage: "again"}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	recorded := systemPrompts(recorder)
	if len(recorded) != 1 {
		t.Fatalf("recorded %d system-prompt events, want 1", len(recorded))
	}
	payload := recorded[0]
	if payload.Text != first.System || payload.Chars != len(first.System) {
		t.Fatalf("recorded prompt does not match the built one: %d chars vs %d", payload.Chars, len(first.System))
	}
	if len(payload.Sections) != len(first.Sections) {
		t.Fatalf("recorded %d sections, want %d", len(payload.Sections), len(first.Sections))
	}
	for i, section := range first.Sections {
		if payload.Sections[i].Name != section.Name || payload.Sections[i].Chars != section.Chars {
			t.Fatalf("section %d = %+v, want %+v", i, payload.Sections[i], section)
		}
	}
	if len(payload.Tools) != 1 || payload.Tools[0] != "shell" {
		t.Fatalf("recorded tools = %v, want [shell]", payload.Tools)
	}
	if len(payload.Skills) != 1 || payload.Skills[0] != "tdd" {
		t.Fatalf("recorded skills = %v, want [tdd]", payload.Skills)
	}
	if payload.Source != "persona" {
		t.Fatalf("recorded source = %q, want %q", payload.Source, "persona")
	}
}

// A /profile swap changes the system message mid-session, so the run records
// the replacement too — otherwise the ndjson would name a prompt the later
// turns never saw.
func TestSetBasePromptRecordsAgain(t *testing.T) {
	recorder := &eventRecorder{}
	builder, err := NewBuilder(Config{BasePrompt: "Old prompt."}, testDeps(Deps{Events: recorder}))
	if err != nil {
		t.Fatalf("NewBuilder() error = %v", err)
	}
	if _, err := builder.Build(context.Background(), Request{SessionID: "s1", UserMessage: "hi"}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	builder.SetBasePrompt("New prompt.")
	if _, err := builder.Build(context.Background(), Request{SessionID: "s1", UserMessage: "hi"}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	recorded := systemPrompts(recorder)
	if len(recorded) != 2 {
		t.Fatalf("recorded %d system-prompt events, want 2", len(recorded))
	}
	if recorded[1].Source != "profile" {
		t.Fatalf("second source = %q, want %q", recorded[1].Source, "profile")
	}
}

// The native tool definitions are the largest slice of a turn's context and are
// not part of the prompt text, so the recorded prompt carries their size from
// the gateway or a reader cannot account for them.
func TestRecordedPromptCarriesNativeSchemaChars(t *testing.T) {
	recorder := &eventRecorder{}
	builder, err := NewBuilder(Config{BasePrompt: "Base prompt."}, testDeps(Deps{
		Events:            recorder,
		NativeSchemaChars: func(string) int { return 1234 },
	}))
	if err != nil {
		t.Fatalf("NewBuilder() error = %v", err)
	}
	if _, err := builder.Build(context.Background(), Request{SessionID: "s1", UserMessage: "hi"}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	recorded := systemPrompts(recorder)
	if len(recorded) != 1 || recorded[0].NativeSchemaChars != 1234 {
		t.Fatalf("recorded native schema chars = %#v, want 1234", recorded)
	}
}
