package toolbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
)

func TestInstallHubToolCopiesAndSetsApproval(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "plugin.json"), []byte(`{"name":"x","policy":{"tiers":["t"],"approval":"always_ask"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	app := config.AppConfig{Toolbox: config.ToolboxConfig{Dir: dst}}
	if err := InstallHubTool(app, HubTool{Name: "x", Dir: src}, "auto"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dst, "x", "plugin.json"))
	if err != nil || !strings.Contains(string(raw), `"approval": "auto"`) {
		t.Fatalf("manifest not rewritten: %s %v", raw, err)
	}
}
