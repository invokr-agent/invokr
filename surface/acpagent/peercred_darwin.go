//go:build darwin

package acpagent

import (
	"errors"

	"golang.org/x/sys/unix"
)

// BSD-family sockets report LOCAL_PEERCRED rather than Linux's SO_PEERCRED.
func peerCredentialUID(fd int) (int, error) {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, err
	}
	if cred == nil {
		return 0, errors.New("no peer credential for this socket")
	}
	return int(cred.Uid), nil
}
