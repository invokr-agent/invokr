package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

// NoOptDefVal makes pflag stop binding a space-separated value, so
// `--resume <id>` arrives as a positional. These cases pin down that all three
// spellings still resolve, and that a mistyped subcommand still fails.
func TestResumeFlagSpellings(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantID   string
		wantFail bool
	}{
		{name: "bare opens the picker", args: []string{"--resume"}, wantID: resumePickSentinel},
		{name: "equals form", args: []string{"--resume=abc"}, wantID: "abc"},
		{name: "space form", args: []string{"--resume", "abc"}, wantID: "abc"},
		{name: "no resume flag", args: []string{}, wantID: ""},
		{name: "mistyped subcommand still fails", args: []string{"agnet"}, wantFail: true},
		{name: "stray arg without resume still fails", args: []string{"--last", "abc"}, wantFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var resumeID string
			var resumeLast bool
			var gotID string
			cmd := &cobra.Command{Use: "invokr", SilenceErrors: true, SilenceUsage: true,
				RunE: func(_ *cobra.Command, args []string) error {
					gotID = resumeIDFromArgs(resumeID, args)
					return nil
				}}
			cmd.Flags().StringVar(&resumeID, "resume", "", "")
			cmd.Flags().BoolVar(&resumeLast, "last", false, "")
			cmd.Flags().Lookup("resume").NoOptDefVal = resumePickSentinel
			cmd.Args = resumeAwareArgs(&resumeID)
			cmd.SetArgs(tc.args)

			err := cmd.Execute()
			if tc.wantFail {
				if err == nil {
					t.Fatalf("args %v should have failed", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("args %v: %v", tc.args, err)
			}
			if gotID != tc.wantID {
				t.Fatalf("resolved session ID = %q, want %q", gotID, tc.wantID)
			}
		})
	}
}
