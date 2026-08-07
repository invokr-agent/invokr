package tools

import (
	"github.com/invokr-ai/invokr/config"
	"path/filepath"
	"testing"
)

func testAppConfigFromEnv(t *testing.T) config.AppConfig {
	t.Helper()
	app, err := config.LoadFile(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	return app
}
