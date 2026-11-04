//go:build linux

package acpagent

import (
	"errors"

	"golang.org/x/sys/unix"
)

// peerUID is the uid of the process at the other end of the accepted socket,
// from SO_PEERCRED. Only a peer that runs as this process may drive a shared
// session, so an unreadable credential refuses the connection.
func peerCredentialUID(fd int) (int, error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	if cred == nil {
		return 0, errors.New("no peer credential for this socket")
	}
	return int(cred.Uid), nil
}
