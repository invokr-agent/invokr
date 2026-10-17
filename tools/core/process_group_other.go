//go:build !unix

package core

import "os/exec"

// ConfigureProcessGroup is a no-op on platforms without unix process groups;
// exec.CommandContext's default kill of the direct child applies.
func ConfigureProcessGroup(_ *exec.Cmd) {}

func awaitGroupGone(_ *exec.Cmd) {}
