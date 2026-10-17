package fsutil

import (
	"fmt"
	"io"
	"os"
)

// CopyFile copies the contents of src to dst, creating or truncating dst with
// the given permission bits and ensuring dst ends with exactly perm (the create
// mode is subject to umask, so it is chmod'ed afterwards to match perm exactly).
func CopyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("copy file: open source: %w", err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("copy file: create destination: %w", err)
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy file: %w", err)
	}
	// Close before chmod: filesystems with deferred allocation (NFS, some fuse
	// mounts) only report write errors such as ENOSPC at close time.
	if err := out.Close(); err != nil {
		return fmt.Errorf("copy file: close destination: %w", err)
	}
	if err := os.Chmod(dst, perm); err != nil {
		return fmt.Errorf("copy file: chmod destination: %w", err)
	}
	return nil
}
