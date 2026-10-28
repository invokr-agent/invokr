package toolbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/invokr-agent/invokr/capability/skills/hub"
	"github.com/invokr-agent/invokr/config"
)

// HubRepo is the repository `invokr toolbox install` and the /tools Install tab read from.
const HubRepo = "invokr-agent/toolbox"

// HubApprovals are the policy.approval values offered at install time.
var HubApprovals = []string{"always_ask", "ask_once", "ask", "auto"}

// HubTool is one installable tools/<name> directory in the hub clone.
type HubTool struct {
	Name, Description, Approval, Dir string
	Installed                        bool
}

// HubTools pulls the hub clone (reusing the skills hub cache) and lists its tools.
func HubTools(ctx context.Context, app config.AppConfig) ([]HubTool, error) {
	app = app.WithDefaults()
	repo, err := hub.NewService(app).Refresh(ctx, HubRepo)
	if err != nil {
		return nil, err
	}
	manifests, _ := filepath.Glob(filepath.Join(repo, "tools", "*", "plugin.json"))
	var found []HubTool
	for _, manifest := range manifests {
		var spec toolboxSpec
		if raw, err := os.ReadFile(manifest); err != nil || json.Unmarshal(raw, &spec) != nil {
			continue
		}
		name := filepath.Base(filepath.Dir(manifest))
		_, missing := os.Stat(filepath.Join(app.Toolbox.Dir, name))
		found = append(found, HubTool{Name: name, Description: spec.Description, Approval: spec.Policy.Approval, Dir: filepath.Dir(manifest), Installed: missing == nil})
	}
	return found, nil
}

// InstallHubTool copies tool into the toolbox dir, replacing an older copy, and writes approval into its manifest.
func InstallHubTool(app config.AppConfig, tool HubTool, approval string) error {
	dst := filepath.Join(app.WithDefaults().Toolbox.Dir, tool.Name)
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.CopyFS(dst, os.DirFS(tool.Dir)); err != nil {
		return err
	}
	path := filepath.Join(dst, "plugin.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		return err
	}
	policy, ok := spec["policy"].(map[string]any)
	if !ok {
		return fmt.Errorf("%s: missing policy", path)
	}
	policy["approval"] = approval
	out, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}
