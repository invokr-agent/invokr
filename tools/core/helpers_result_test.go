package core

import (
	"errors"
	"strings"
	"testing"
)

func TestMarshalResult(t *testing.T) {
	out, err := MarshalResult("shell", map[string]int{"a": 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != `{"a":1}` {
		t.Fatalf("got %q", out)
	}

	_, err = MarshalResult("shell", make(chan int))
	if err == nil {
		t.Fatalf("expected error marshaling channel")
	}
	if !strings.HasPrefix(err.Error(), "encode shell result: ") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestOutcome(t *testing.T) {
	if got := Outcome(nil); got != "completed" {
		t.Fatalf("Outcome(nil)=%q want completed", got)
	}
	if got := Outcome(errors.New("boom")); got != "failed" {
		t.Fatalf("Outcome(err)=%q want failed", got)
	}
}
