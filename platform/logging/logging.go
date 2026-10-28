package logging

import (
	"fmt"
	"github.com/invokr-agent/invokr/config"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type rule struct {
	name  string
	level zapcore.Level
}

type filterCore struct {
	zapcore.Core
	defaultLevel, minLevel zapcore.Level
	rules                  []rule
}

func NewWithSink(cfg config.LoggingConfig, sink io.Writer) (*zap.Logger, error) {
	cfg.Level = orBlank(cfg.Level, config.DefaultLevel)
	cfg.Format = orBlank(cfg.Format, config.DefaultFormat)
	cfg.Output = orBlank(cfg.Output, config.DefaultOutput)
	if err := ensureOutputDir(cfg.Output); err != nil {
		return nil, err
	}
	if err := truncateDefaultOutput(cfg.Output); err != nil {
		return nil, err
	}

	defaultLevel, rules, err := parseProfile(cfg.Level, cfg.Filter)
	if err != nil {
		return nil, err
	}

	minLevel := defaultLevel
	for _, r := range rules {
		minLevel = min(minLevel, r.level)
	}

	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.EncodeTime, encoderCfg.EncodeDuration = zapcore.ISO8601TimeEncoder, zapcore.StringDurationEncoder

	zapCfg := zap.Config{
		Level: zap.NewAtomicLevelAt(minLevel), Development: false, Encoding: cfg.Format,
		EncoderConfig: encoderCfg, OutputPaths: []string{cfg.Output}, ErrorOutputPaths: []string{"stderr"}, DisableStacktrace: true,
	}

	switch cfg.Format {
	case "json":
	case "console":
		zapCfg.EncoderConfig = zap.NewDevelopmentEncoderConfig()
		zapCfg.EncoderConfig.EncodeTime, zapCfg.EncoderConfig.EncodeDuration = zapcore.ISO8601TimeEncoder, zapcore.StringDurationEncoder
	default:
		return nil, fmt.Errorf("invalid log format %q: expected console or json", cfg.Format)
	}

	return zapCfg.Build(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		if sink != nil {
			core = zapcore.NewTee(core, zapcore.NewCore(
				encoderForFormat(cfg.Format, zapCfg.EncoderConfig),
				zapcore.AddSync(sink),
				zapCfg.Level,
			))
		}
		return &filterCore{Core: core, defaultLevel: defaultLevel, minLevel: minLevel, rules: rules}
	}))
}

// orBlank returns value unless it is blank, in which case it returns fallback.
// Unlike cmp.Or the value is kept verbatim, whitespace included.
func orBlank(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func truncateDefaultOutput(output string) error {
	if filepath.Clean(output) != filepath.Clean(config.DefaultLogPath()) {
		return nil
	}
	err := os.Truncate(output, 0)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func ensureOutputDir(output string) error {
	switch strings.TrimSpace(output) {
	case "", "stderr", "stdout":
		return nil
	}
	dir := filepath.Dir(output)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

func encoderForFormat(format string, cfg zapcore.EncoderConfig) zapcore.Encoder {
	if format == "console" {
		return zapcore.NewConsoleEncoder(cfg)
	}
	return zapcore.NewJSONEncoder(cfg)
}

func (c *filterCore) Enabled(level zapcore.Level) bool {
	return level >= c.minLevel && c.Core.Enabled(level)
}

func (c *filterCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if entry.Level < c.levelFor(entry.LoggerName) {
		return ce
	}
	return c.Core.Check(entry, ce)
}

func (c *filterCore) With(fields []zapcore.Field) zapcore.Core {
	clone := *c
	clone.Core = c.Core.With(fields)
	return &clone
}

func (c *filterCore) levelFor(name string) zapcore.Level {
	for _, r := range c.rules {
		if matches(r.name, name) {
			return r.level
		}
	}
	return c.defaultLevel
}

func matches(pattern, name string) bool {
	pattern = strings.TrimSpace(pattern)
	return pattern == "" || pattern == "*" || name == pattern || strings.HasPrefix(name, pattern+".") || strings.HasPrefix(name, pattern+"/") || (strings.HasSuffix(pattern, "*") && strings.HasPrefix(name, strings.TrimSuffix(pattern, "*")))
}

func parseProfile(level, filter string) (zapcore.Level, []rule, error) {
	var defaultLevel zapcore.Level
	if err := defaultLevel.Set(strings.TrimSpace(level)); err != nil {
		return 0, nil, fmt.Errorf("invalid log level %q: %w", level, err)
	}

	if strings.TrimSpace(filter) == "" {
		return defaultLevel, nil, nil
	}

	var rules []rule
	for part := range strings.SplitSeq(filter, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, rawLevel, ok := strings.Cut(part, "=")
		if !ok {
			rawLevel, name, ok = strings.Cut(part, ":")
		}
		if !ok {
			return 0, nil, fmt.Errorf("invalid log filter rule %q: expected name=level or level:name", part)
		}
		var lvl zapcore.Level
		if err := lvl.Set(strings.TrimSpace(rawLevel)); err != nil {
			return 0, nil, fmt.Errorf("invalid log filter level in %q: %w", part, err)
		}
		rules = append(rules, rule{name: strings.TrimSpace(name), level: lvl})
	}
	return defaultLevel, rules, nil
}
