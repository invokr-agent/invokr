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

	if !confirmPurge(cmd) {
		t.Fatal("confirmPurge rejected yes at EOF")
	}
}
