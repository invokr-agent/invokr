package agentui

import (
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/clients"
	"github.com/invokr-ai/invokr/platform/turn"
)

type reasoningFake struct {
	turn.Session
	state clients.ReasoningState
}

func (f *reasoningFake) SetReasoningEffort(level string) error {
	f.state.Level = level
	return nil
}
func (f *reasoningFake) ReasoningEffort() clients.ReasoningState { return f.state }

func TestReasoningCommandAndLabel(t *testing.T) {
	for name, tc := range map[string]struct {
		state     clients.ReasoningState
		wantMsg   string
		wantLabel string
	}{
		"supported":   {clients.ReasoningState{Kind: clients.ReasoningLlamaCpp, Supported: true}, "reasoning effort: high (applies from next turn)", "high"},
		"unsupported": {clients.ReasoningState{Kind: clients.ReasoningLlamaCpp}, reasoningUnsupported, "high ⚠"},
		"off scale":   {clients.ReasoningState{Kind: clients.ReasoningOllama}, "not in the ollama scale", "xhigh ⚠"},
		"unknown":     {clients.ReasoningState{}, "no reasoning scale for this provider", ""},
	} {
		s := &reasoningFake{state: tc.state}
		level := strings.TrimSuffix(tc.wantLabel, " ⚠")
		if level == "" {
			level = "high"
		}
		msg := reasoningFeedback(s, []string{level})
		if !strings.Contains(msg, tc.wantMsg) {
			t.Fatalf("%s: message %q lacks %q", name, msg, tc.wantMsg)
		}
		if got := reasoningLabel(s); got != tc.wantLabel {
			t.Fatalf("%s: label = %q, want %q", name, got, tc.wantLabel)
		}
	}
	if msg := reasoningFeedback(&reasoningFake{}, nil); msg != "reasoning effort: default; no reasoning scale for this provider (llama.cpp, Ollama, OpenRouter, Anthropic), not sent" {
		t.Fatalf("bare /reasoning = %q", msg)
	}
	if msg := reasoningFeedback(&reasoningFake{state: clients.ReasoningState{Kind: clients.ReasoningOllama}}, nil); msg != "reasoning effort: default (ollama: none|low|medium|high|max)" {
		t.Fatalf("bare /reasoning on ollama = %q", msg)
	}
}

func TestReasoningLabelDefaultsWhenProviderHasScale(t *testing.T) {
	if got := reasoningLabel(&reasoningFake{state: clients.ReasoningState{Kind: clients.ReasoningLlamaCpp}}); got != "default" {
		t.Fatalf("label = %q, want %q", got, "default")
	}
	if got := reasoningLabel(&reasoningFake{state: clients.ReasoningState{}}); got != "" {
		t.Fatalf("label with no known scale = %q, want empty", got)
	}
}
