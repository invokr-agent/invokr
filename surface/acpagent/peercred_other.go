//go:build unix && !linux && !darwin

package acpagent

import "errors"

// No socket peer credential on this OS: sharing a session refuses an unverified peer.
func peerCredentialUID(int) (int, error) {
	return 0, errors.New("checking the socket's owner is unsupported on this OS")
}
