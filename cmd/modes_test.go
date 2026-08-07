package cmd

import (
	"io"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
)

func TestStartupModeFlags(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		mode    approval.Mode
		invalid bool
	}{
		{"default", nil, approval.Manual, false},
		{"auto", []string{"agent", "--mode", "auto"}, approval.Auto, false},
		{"plan", []string{"exec", "--mode=plan", "inspect"}, approval.Plan, false},
		{"yolo", []string{"--yolo"}, approval.YOLO, false},
		{"legacy", []string{"exec", "--allow-all", "task"}, approval.Manual, false},
		{"no-mode-yolo", []string{"--mode=yolo"}, "", true},
		{"conflict", []string{"--yolo", "--mode=auto"}, "", true},
		{"legacy-conflict", []string{"exec", "--allow-all", "--mode=auto", "task"}, "", true},
		{"wrong-command", []string{"doctor", "--mode=auto"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := NewRootCmd(config.AppConfig{})
			root.SetErr(io.Discard)
			command, args, err := root.Find(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if err := command.ParseFlags(args); err != nil {
				t.Fatal(err)
			}
			p, err := startupMode(command)
			if (err != nil) != tc.invalid {
				t.Fatalf("policy=%v error=%v", p, err)
			}
			if !tc.invalid && p.Mode() != tc.mode {
				t.Fatalf("mode=%s want %s", p.Mode(), tc.mode)
			}
		})
	}
}

func TestYOLOPreflightUsesFlagParser(t *testing.T) {
	for _, tc := range []struct {
		args              []string
		disabled, invalid bool
	}{
		{[]string{"--yolo"}, true, false},
		{[]string{"exec", "--yolo", "task"}, true, false},
		{[]string{"exec", "--", "--yolo"}, false, false},
		{[]string{"--model", "--yolo"}, false, false},
		{[]string{"--yolo=false"}, false, false},
		{[]string{"--yolo", "--sandbox=false"}, false, true},
		{[]string{"doctor", "--yolo"}, false, true},
	} {
		cfg := config.AppConfig{}.WithDefaults()
		cfg.Sandbox.Enabled = true
		err := ConfigureStartupModes(&cfg, tc.args)
		if (err != nil) != tc.invalid || cfg.Sandbox.Enabled == tc.disabled {
			t.Errorf("%v: enabled=%v err=%v", tc.args, cfg.Sandbox.Enabled, err)
		}
	}
}
