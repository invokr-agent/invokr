package clients

import (
	"fmt"
	"testing"
)

func TestNormalizeReasoningEffort(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"": "", "default": "", " High ": "high", "none": "none"} {
		if got, err := NormalizeReasoningEffort(in); err != nil || got != want {
			t.Fatalf("normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := NormalizeReasoningEffort("bogus"); err == nil {
		t.Fatal("normalize(bogus) = nil error")
	}
}

func TestReasoningKindScales(t *testing.T) {
	t.Parallel()
	for kind, want := range map[ReasoningKind]string{
		ReasoningLlamaCpp:   "reasoning_effort=high",
		ReasoningOllama:     "reasoning_effort=high",
		ReasoningOpenRouter: "reasoning=map[effort:high]",
		ReasoningAnthropic:  "output_config=map[effort:high]",
	} {
		key, value := kind.Extra("high")
		if got := fmt.Sprintf("%s=%v", key, value); got != want {
			t.Fatalf("%s: Extra = %s, want %s", kind, got, want)
		}
	}
	if ReasoningOllama.Accepts("xhigh") || ReasoningAnthropic.Accepts("none") || !ReasoningLlamaCpp.Accepts("xhigh") {
		t.Fatal("Accepts() does not follow the per-kind scale")
	}
	if ReasoningKind("").Accepts("high") {
		t.Fatal("unknown kind accepts a level")
	}
	if !ReasoningLlamaCpp.DisablesThinking("none") || ReasoningLlamaCpp.DisablesThinking("high") || ReasoningOllama.DisablesThinking("none") {
		t.Fatal("DisablesThinking() does not follow llama.cpp none")
	}
}
