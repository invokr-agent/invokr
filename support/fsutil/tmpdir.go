package fsutil

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// SharedTmpDir names the scratch directory the shell sandbox mounts at /tmp
// and code_interpreter reaches under the same path. It is keyed by the
// workspace root but lives under the host temp dir: a directory inside the
// workspace survived the run and showed up in the task repo's git status and
// in the model's own directory listings (round-8 bench).
func SharedTmpDir(workspaceRoot string) string {
	if workspaceRoot == "" {
		return ""
	}
	abs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		abs = workspaceRoot
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(os.TempDir(), "invokr-tmp-"+hex.EncodeToString(sum[:8]))
}
