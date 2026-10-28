package toolbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/tools/core"
)

// Check before venv creation, dependency resolution, or lock-file writes.
func (p *executableTool) approvePreparation(ctx context.Context) error {
	reqPath := filepath.Join(p.dir, "requirements.txt")
	contents, _, hash, err := readValidatedRequirements(reqPath, p.config)
	if err != nil || contents == nil {
		return err
	}
	if approval.ModeFromContext(ctx) == approval.Manual {
		return nil
	}
	venv := pythonVenvDir(p.dir, p.config)
	cache, _, err := readRequirementsCache(requirementsMarkerPath(venv, reqPath, p.config))
	if err != nil {
		return err
	}
	_, statErr := os.Stat(filepath.Join(venv, "bin", "python"))
	if cache.Hash == hash && statErr == nil {
		return nil
	}
	if approval.ModeFromContext(ctx) == approval.Plan {
		return fmt.Errorf("plan mode does not prepare Python dependencies")
	}
	return core.EnforceApproval(ctx, p.approver, p.ToolSpec, core.Ask(approval.Request{Tool: p.Name, Subject: "prepare " + p.dir,
		Writes: true, Protected: true, Network: true, Summary: "Prepare Python dependencies", Details: "May write " + venv + " and " + filepath.Join(p.dir, "requirements.lock")}))
}
