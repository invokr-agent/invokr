// Package fsutil provides small filesystem helpers shared across the tools
// and internal/memory trees. It must stay a leaf package (no imports of
// github.com/invokr-ai/invokr/tools/* or github.com/invokr-ai/invokr/capability/memory/*) so both sides can
// depend on it without creating an import cycle.
package fsutil

import (
	"errors"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path by creating a temp file in path's
// directory, writing the contents, and renaming it into place. If perm is
// non-zero the temp file is chmod'ed to perm before the rename; a zero perm
// leaves the temp file's default (umask-derived) permissions in place. The
// temp file is removed if any step before the rename fails. After the
// rename, the containing directory is fsync'ed so the new directory entry
// is durable too — without this, a crash between the rename and the next
// natural directory flush can lose the write despite the file-level Sync.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	_, err = tmp.Write(data)
	if err == nil && perm != 0 {
		err = tmp.Chmod(perm)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return syncDir(dir)
}

// syncDir opens dir and fsyncs it, making directory-entry changes (e.g. a
// rename into dir) durable.
func syncDir(dir string) (err error) {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	return f.Sync()
}
