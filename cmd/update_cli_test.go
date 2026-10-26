package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/invokr-ai/invokr/platform/version"

	"github.com/spf13/cobra"
)

// seen records the versions the injected installer was handed. installer.Run
// downloads a real release over a real binary, so tests never call it: they
// swap the package var and assert what the command owns.
var seen []string

func stub(failure error) func(context.Context, string, bool, io.Writer) error {
	return func(ctx context.Context, wanted string, _ bool, out io.Writer) error {
		seen = append(seen, wanted)
		_, _ = fmt.Fprintf(out, "would install %s\n", wanted)
		return failure
	}
}

func updateFor(t *testing.T, failure error) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	original, stamped := runInstaller, version.Version
	seen = []string{}
	t.Cleanup(func() { seen = []string{}; runInstaller = original; version.Version = stamped })
	version.Version = "v0.0.1"
	runInstaller = stub(failure)
	out := &bytes.Buffer{}
	command := NewUpdateCmd()
	command.SetOut(out)
	command.SetErr(out)
	return command, out
}

func TestUpdatePassesTheRequestedVersion(t *testing.T) {
	command, out := updateFor(t, nil)
	command.SetArgs([]string{"v0.2.1"})
	if err := command.Execute(); err != nil {
		t.Fatalf("update v0.2.1: %v (out = %q)", err, out.String())
	}
	if len(seen) != 1 || seen[0] != "v0.2.1" {
		t.Fatalf("installer got %v, want [v0.2.1]", seen)
	}
}

func TestUpdateOmittedVersionMeansNewest(t *testing.T) {
	command, out := updateFor(t, nil)
	command.SetArgs([]string{})
	if err := command.Execute(); err != nil {
		t.Fatalf("update: %v (out = %q)", err, out.String())
	}
	if len(seen) != 1 || seen[0] != "" {
		t.Fatalf("installer got %v, want one empty version", seen)
	}
}

func TestUpdateRefusesDevBuild(t *testing.T) {
	command, _ := updateFor(t, nil)
	version.Version = "dev"
	command.SetArgs([]string{})
	if err := command.Execute(); err == nil {
		t.Fatal("want an error for a dev build")
	}
	if len(seen) != 0 {
		t.Fatalf("dev build ran the installer: %v", seen)
	}
}

func TestUpdateAddsTheVPrefix(t *testing.T) {
	command, out := updateFor(t, nil)
	command.SetArgs([]string{"0.2.1"})
	if err := command.Execute(); err != nil {
		t.Fatalf("update 0.2.1: %v (out = %q)", err, out.String())
	}
	if len(seen) != 1 || seen[0] != "v0.2.1" {
		t.Fatalf("installer got %v, want [v0.2.1]", seen)
	}
}
