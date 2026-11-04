package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/support/fsutil"
	"github.com/invokr-agent/invokr/tools/core"
)

type missingSeal struct {
	*fsutil.AbsentPath
	root  string
	epoch uint64
	asked bool // this call would repeat a consent question already answered
}

type sealOwner struct {
	epoch  uint64
	active int
	asked  bool // the process already consented to creating names under this root
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
			owner := sealOwners.roots[root]
			missing = append(missing, missingSeal{absent, root, owner.epoch, owner.asked})
			continue
		}
		existing = append(existing, path)
	}
	return existing, missing, nil
}

func approveSeals(ctx context.Context, missing []missingSeal) error {
	// Consent is remembered per writable root for the whole process: the same
	// absent names reappear on every call, and a second prompt after the
	// tool's own made every write in a worktree look like a hang (#91). A
	// refusal is not remembered, and the policy check still runs once the
	// answer is on record, so a session with no approver still refuses.
	if len(missing) == 0 {
		return nil
	}
	req := approval.CallRequest(ctx)
	req.Confined, req.Writes, req.ReadOnly = true, true, false
	req.Protected = slices.ContainsFunc(missing, func(m missingSeal) bool { return !m.asked })
	paths := core.MapSlice(missing, func(m missingSeal) string { return m.Path })
	req.Summary = "Let this workspace create protected paths (.git hooks or config, .invokr) that do not exist yet"
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
			// Reaching this point means the call was approved or auto-approved,
			// so the answer about creating these names is on record for the
			// rest of the process (#91). A refusal never gets here.
			state.asked = true
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
