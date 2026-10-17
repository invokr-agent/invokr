package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// AbsentPath pins the existing parent of a name observed absent with Lstat.
// The caller closes Parent after checking/removing the name.
type AbsentPath struct {
	Path   string
	Parent *os.Root
}

func SnapshotAbsent(path string) (*AbsentPath, error) {
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return nil, err
	}
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if _, err := parent.Lstat(filepath.Base(path)); !os.IsNotExist(err) {
		_ = parent.Close()
		return nil, err
	}
	return &AbsentPath{path, parent}, nil
}

// Appeared refuses redirected parents, including symlinks to another directory
// inside the workspace. RemoveAll must never delete that directory's old files.
func (p *AbsentPath) Appeared() (bool, error) {
	original, err := p.Parent.Stat(".")
	if err != nil {
		return false, err
	}
	current, err := os.Stat(filepath.Dir(p.Path))
	if err != nil || !os.SameFile(original, current) {
		return false, fmt.Errorf("protected path parent changed: %s; cleanup skipped", p.Path)
	}
	_, err = p.Parent.Lstat(filepath.Base(p.Path))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func (p *AbsentPath) Remove() error { return p.Parent.RemoveAll(filepath.Base(p.Path)) }
