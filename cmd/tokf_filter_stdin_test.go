package cmd

import (
	"bytes"
	"testing"
)

func TestTokfFilterStdinCmdCopiesInputToOutput(t *testing.T) {
	cmd := NewTokfFilterStdinCmd()
	var stdout bytes.Buffer
	cmd.SetIn(bytes.NewBufferString("raw tool result\nline two"))
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--tool", "read_file", "--source", "toolbox", "--runtime", "python"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if stdout.String() != "raw tool result\nline two" {
		t.Fatalf("expected stdin passthrough, got %q", stdout.String())
	}
}
