package markdown

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"github.com/invokr-agent/invokr/support/fsutil"
)

const fileLockRetryDelay = 10 * time.Millisecond

type atomicFile struct {
	path    string
	fileMod os.FileMode
	dirMod  os.FileMode
	mu      sync.Mutex
}

func (f *atomicFile) Read() ([]byte, error) { return os.ReadFile(f.path) }

func (f *atomicFile) WithLock(ctx context.Context, update func([]byte) ([]byte, bool, error)) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.path), f.dirMod); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	lock := flock.New(f.path+".lock", flock.SetPermissions(f.fileMod))
	locked, err := lock.TryLockContext(ctx, fileLockRetryDelay)
	if err != nil {
		return err
	}
	if !locked {
		return ctx.Err()
	}
	defer func() {
		if unlockErr := lock.Unlock(); unlockErr != nil {
			err = errors.Join(err, unlockErr)
		}
	}()

	if err := ctx.Err(); err != nil {
		return err
	}

	current, err := os.ReadFile(f.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	next, changed, err := update(current)
	if err != nil || !changed {
		return err
	}

	return fsutil.WriteFileAtomic(f.path, next, f.fileMod)
}
