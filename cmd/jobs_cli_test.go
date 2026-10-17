package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestConfirmPurgeAcceptsInputWithoutNewline(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("yes"))
	cmd.SetOut(&bytes.Buffer{})

	if !confirm(cmd, "This will remove all jobs.") {
		t.Fatal("confirm rejected yes at EOF")
	}
}
