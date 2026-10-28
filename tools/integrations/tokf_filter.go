package integrations

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/invokr-agent/invokr/support/strutil"
	"github.com/invokr-agent/invokr/tools/core"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"

	"go.uber.org/zap"
)

type TokfResultFilter struct {
	bin, home, helperBin string
	logger               *zap.Logger
}

func ResultFilterForConfig(cfg config.ToolboxConfig, logger *zap.Logger) core.ResultFilter {
	if cfg.TokfDisabled {
		return core.NoopResultFilter{}
	}

	bin := strutil.FirstNonEmpty(cfg.TokfBin, constants.DefaultTokfBin)
	if bin == constants.DefaultTokfBin && runningFromGoTestBinary() {
		logger.Debug("tokf result filtering disabled in go test binary")
		return core.NoopResultFilter{}
	}
	resolvedBin, err := lookupTokfBin(bin)
	if err != nil {
		logger.Warn("tokf result filtering disabled", zap.String("bin", bin), zap.Error(err))
		return core.NoopResultFilter{}
	}
	cfg.TokfBin = resolvedBin
	return NewTokfResultFilter(cfg, logger)
}

func NewTokfResultFilter(cfg config.ToolboxConfig, logger *zap.Logger) *TokfResultFilter {
	if logger == nil {
		logger = zap.NewNop()
	}

	helperBin, err := os.Executable()
	if err != nil || strings.TrimSpace(helperBin) == "" {
		helperBin = "github.com/invokr-agent/invokr"
	}

	return &TokfResultFilter{bin: strutil.FirstNonEmpty(cfg.TokfBin, constants.DefaultTokfBin), home: cfg.TokfHome, helperBin: helperBin, logger: logger}
}

func (f *TokfResultFilter) FilterToolResult(ctx context.Context, meta core.ToolResultMeta, input string) (string, error) {
	args := []string{"run", strutil.FirstNonEmpty(f.helperBin, "github.com/invokr-agent/invokr"), "tokf-filter-stdin", "--tool", meta.ToolName, "--source", meta.Source}
	if strings.TrimSpace(meta.Runtime) != "" {
		args = append(args, "--runtime", meta.Runtime)
	}

	cmd := exec.CommandContext(ctx, strutil.FirstNonEmpty(f.bin, constants.DefaultTokfBin), args...)
	core.ConfigureProcessGroup(cmd)
	cmd.Env = tokfEnv(os.Environ(), f.home)
	cmd.Stdin = strings.NewReader(input)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if f.logger != nil {
			f.logger.Warn("tokf result filtering failed", zap.String("tool", meta.ToolName), zap.String("source", meta.Source), zap.String("stderr", strings.TrimSpace(stderr.String())), zap.Error(err))
		}
		return input, nil
	}
	if ctx.Err() != nil {
		// A cancelled run can still exit cleanly: the helper's own wait returns
		// once its children are killed, so the process reports success and stdout
		// holds whatever partial output it printed, not the filtered result. The
		// context says the run was cut short, so hand back the unfiltered input
		// rather than a truncated answer that looks filtered.
		return input, nil
	}

	return stdout.String(), nil
}

func runningFromGoTestBinary() bool {
	exe, err := os.Executable()
	return err == nil && strings.HasSuffix(filepath.Base(exe), ".test")
}

func lookupTokfBin(bin string) (string, error) {
	bin = strutil.FirstNonEmpty(bin, constants.DefaultTokfBin)
	resolvedBin, err := exec.LookPath(bin)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(bin) || strings.ContainsRune(bin, os.PathSeparator) {
		return bin, nil
	}
	return resolvedBin, nil
}

func tokfEnv(env []string, home string) []string {
	if strings.TrimSpace(home) == "" {
		return env
	}
	return core.WithEnvValue(env, "TOKF_HOME", home)
}
