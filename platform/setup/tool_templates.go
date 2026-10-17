package setup

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/support/fsutil"
)

//go:embed tool-presets/*.json
var toolTemplates embed.FS

func ToolTemplatesDir(homeDir string) string { return config.ProfilesDir(homeDir) }

// WriteToolTemplates seeds missing editable JSON files, or replaces them on reset.
func WriteToolTemplates(homeDir string, reset bool) error {
	dir := ToolTemplatesDir(homeDir)
	if err := os.MkdirAll(dir, homeDirPerm); err != nil {
		return err
	}
	paths, err := fs.Glob(toolTemplates, "tool-presets/*.json")
	if err != nil {
		return err
	}
	for _, path := range paths {
		target := filepath.Join(dir, filepath.Base(path))
		if exists, err := fsutil.Exists(target); err != nil || exists && !reset {
			if err != nil {
				return err
			}
			continue
		}
		data, err := toolTemplates.ReadFile(path)
		if err != nil {
			return err
		}
		if err := fsutil.WriteFileAtomic(target, data, configFilePerm); err != nil {
			return err
		}
	}
	return nil
}
