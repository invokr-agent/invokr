package tools

import (
	"github.com/invokr-ai/invokr/config"
	"testing"
)

func testAppConfigFromEnv(t *testing.T) config.AppConfig {
	t.Helper()
	t.Setenv("INVOKR_HOME", t.TempDir())
	app, err := config.LoadDefaultFileWithProfile("")
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	return app
}
