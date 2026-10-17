package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/support/fsutil"
	"github.com/invokr-ai/invokr/tools/core"
)

type missingSeal struct {
	*fsutil.AbsentPath
	root  string
	epoch uint64
}

type sealOwner struct {
	epoch  uint64
	active int
}

// ponytail: process-wide bookkeeping; keep epochs after exit so an overlapping
// foreground cleanup cannot erase an approved process's completed creation.
var sealOwners = struct {
	sync.Mutex
	roots map[string]sealOwner
}{roots: make(map[string]sealOwner)}

// ProcessSpec opts foreground execution into Auto's post-run protected-name check.
// Other callers use Command and retain per-launch approval for absent seals.
func ProcessSpec(plan Plan, dir string, args []string, oneShot bool) core.ProcessSpec {
	return core.ProcessSpec{Name: plan.CommandPath, Dir: dir, Args: args,
		Build: func(ctx context.Context) (*exec.Cmd, func() error, error) {
			return prepareCommand(ctx, plan, dir, args, oneShot && approval.ModeFromContext(ctx) == approval.Auto)
		},
	}
}

func Command(ctx context.Context, plan Plan, dir string, args []string) (*exec.Cmd, func(), error) {
	cmd, finish, err := prepareCommand(ctx, plan, dir, args, false)
	if err != nil {
		return nil, nil, err
	}
	return cmd, func() { _ = finish() }, nil
}

func prepareCommand(ctx context.Context, plan Plan, dir string, args []string, oneShot bool) (*exec.Cmd, func() error, error) {
	existing, missing, err := snapshotSeals(plan)
	closeRoots := func() {
		for _, m := range missing {
			_ = m.Parent.Close()
		}
	}
	if err == nil && !oneShot {
		err = approveSeals(ctx, missing)
	}
	var cmd *exec.Cmd
	var cleanup func()
	if err == nil {
		plan.Sealed = existing
		cmd, cleanup, err = command(ctx, plan, dir, args)
	}
	if err != nil {
		closeRoots()
		return nil, nil, err
	}
	if !oneShot {
		retainSeals(missing, 1)
	}
	return cmd, func() error {
		defer cleanup()
		defer closeRoots()
		if oneShot {
			return checkSeals(missing)
		}
		retainSeals(missing, -1)
		return nil
	}, nil
}

func snapshotSeals(plan Plan) (existing []string, missing []missingSeal, err error) {
	sealOwners.Lock()
	defer sealOwners.Unlock()
	var processed []string
	for _, path := range slices.Sorted(slices.Values(plan.Sealed)) {
		i := slices.IndexFunc(plan.RW, func(root string) bool { _, inside := fsutil.RelInside(root, path); return inside })
		if path == "" || i < 0 {
			continue
		}
		// Only an ancestor actually sealed beneath an RW mount covers children.
		if slices.ContainsFunc(processed, func(p string) bool { return p == path || strings.HasPrefix(path, p+"/") }) {
			continue
		}
		processed = append(processed, path)
		root := plan.RW[i]
		absent, statErr := fsutil.SnapshotAbsent(path)
		if statErr != nil {
			return existing, missing, statErr
		}
		if absent != nil {
			missing = append(missing, missingSeal{absent, root, sealOwners.roots[root].epoch})
			continue
		}
		existing = append(existing, path)
	}
	return existing, missing, nil
}

func approveSeals(ctx context.Context, missing []missingSeal) error {
	if len(missing) == 0 {
		return nil
	}
	paths := core.MapSlice(missing, func(m missingSeal) string { return m.Path })
	req := approval.CallRequest(ctx)
	req.Protected, req.Confined, req.Writes, req.ReadOnly = true, true, true, false
	req.Summary = "Run with unsealed protected names"
	req.Details = "These paths do not exist and cannot be sealed. This process and its children could create or modify them:\n" + strings.Join(paths, "\n")
	handled, err := approval.Authorize(ctx, nil, req, true)
	if !handled {
		return fmt.Errorf("approval required for missing protected paths: %s", strings.Join(paths, ", "))
	}
	return err
}

func retainSeals(missing []missingSeal, delta int) {
	sealOwners.Lock()
	defer sealOwners.Unlock()
	for _, m := range missing {
		state := sealOwners.roots[m.root]
		state.active += delta
		if delta > 0 {
			state.epoch++
		}
		sealOwners.roots[m.root] = state
	}
}

func checkSeals(missing []missingSeal) error {
	sealOwners.Lock()
	defer sealOwners.Unlock()
	var failures []error
	for _, m := range missing {
		appeared, err := m.Appeared()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !appeared {
			continue
		}
		state := sealOwners.roots[m.root]
		if state.active > 0 || state.epoch != m.epoch {
			failures = append(failures, fmt.Errorf("protected path %s appeared; retained because approved execution overlapped", m.Path))
		} else if err := m.Remove(); err != nil {
			failures = append(failures, fmt.Errorf("remove newly created protected path %s: %w", m.Path, err))
		} else {
			failures = append(failures, fmt.Errorf("removed newly created protected path %s", m.Path))
		}
	}
	return errors.Join(failures...)
}
