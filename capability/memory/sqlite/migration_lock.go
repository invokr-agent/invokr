package sqlite

import "github.com/gofrs/flock"

// lockMigrationFile serializes first-boot migrations across processes.
// It blocks until the exclusive lock on path is acquired, matching the
// previous platform-specific flock(2)/LockFileEx implementations.
//
// flock.New opens the lock file with O_CREATE|O_RDONLY on linux/darwin/*bsd
// (O_CREATE|O_RDWR on aix/solaris/illumos) rather than the O_CREATE|O_RDWR
// the hand-rolled code used; both lock syscalls accept a read-only handle,
// so the exclusive-lock semantics are unchanged.
//
// The returned func releases the lock and, on success, closes the underlying
// file. If the unlock syscall itself fails the handle is left open — an error
// this code cannot act on, and one bounded by process exit.
func lockMigrationFile(path string) (func(), error) {
	lock := flock.New(path)
	if err := lock.Lock(); err != nil {
		return nil, err
	}
	return func() { _ = lock.Unlock() }, nil
}
