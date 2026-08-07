//go:build !linux

package sandbox

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/invokr-ai/invokr/tools/core"
)

// Plan mirrors the Linux variant's field set (RO/RW included)
// so toolbox.go's platform-agnostic call sites compile here too. Namespace
// sandboxing itself is Linux-only (see Command below), so the path
// fields are never read on this build.
type Plan struct {
	Sealed              []string
	Network             bool
	RO, RW, AllowHosts  []string
	CommandPath, TmpDir string
	MemoryLimit         int64
	Detached            bool
}

func BuildPlan(cmdPath string, _ string, _ string, _ string, _ core.ToolCapabilities, memoryLimitBytes int64, _ []string) Plan {
	return Plan{CommandPath: cmdPath, MemoryLimit: memoryLimitBytes}
}

// DedupPlan is a no-op here: mirrors the Linux variant's signature for
// toolbox.go's shared call sites.
func DedupPlan(plan Plan) Plan { return plan }

func command(_ context.Context, _ Plan, _ string, _ []string) (*exec.Cmd, func(), error) {
	return nil, nil, fmt.Errorf("namespace sandboxing is only implemented on Linux")
}

func Available() bool { return false }
