package cmd

import (
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
	appruntime "github.com/invokr-agent/invokr/platform/runtime"
)

func TestACPCmdRefusesStdoutLogging(t *testing.T) {
	cmd := NewACPCmd(func() (appruntime.Services, error) {
		return appruntime.Services{App: config.AppConfig{Log: config.LoggingConfig{Output: "stdout"}}}, nil
	})
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "stdout carries the protocol") {
		t.Fatalf("err = %v, want stdout refusal", err)
	}
}
