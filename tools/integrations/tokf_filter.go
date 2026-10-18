package integrations

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/invokr-ai/invokr/support/strutil"
	"github.com/invokr-ai/invokr/tools/core"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"

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
		helperBin = "github.com/invokr-ai/invokr"
	}

	return &TokfResultFilter{bin: strutil.FirstNonEmpty(cfg.TokfBin, constants.DefaultTokfBin), home: cfg.TokfHome, helperBin: helperBin, logger: logger}
}

func (f *TokfResultFilter) FilterToolResult(ctx context.Context, meta core.ToolResultMeta, input string) (string, error) {
	args := []string{"run", strutil.FirstNonEmpty(f.helperBin, "github.com/invokr-ai/invokr"), "tokf-filter-stdin", "--tool", meta.ToolName, "--source", meta.Source}
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

	// A cancelled run can exit 0 when the group kill races the shell's own
	// exit, so the context is checked too.
	if err := cmd.Run(); err != nil || ctx.Err() != nil {
		if f.logger != nil {
			f.logger.Warn("tokf result filtering failed", zap.String("tool", meta.ToolName), zap.String("source", meta.Source), zap.String("stderr", strings.TrimSpace(stderr.String())), zap.Error(err), zap.NamedError("ctx", ctx.Err()))
		}
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
