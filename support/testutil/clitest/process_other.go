//go:build !unix

package clitest

import "os/exec"

func configureCommand(_ *exec.Cmd) {}
