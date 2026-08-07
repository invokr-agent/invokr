package config

import "path/filepath"

const (
	DefaultLevel   = "warn"
	DefaultFormat  = "console"
	DefaultOutput  = "stderr"
	DefaultLogFile = "invokr.log"
)

type LoggingConfig struct {
	Level  string `json:"level,omitempty"`
	Format string `json:"format,omitempty"`
	Output string `json:"output,omitempty"`
	Filter string `json:"filter,omitempty"`
}

func DefaultLogPath() string { return filepath.Join(InvokrHomeDir(), DefaultLogFile) }
