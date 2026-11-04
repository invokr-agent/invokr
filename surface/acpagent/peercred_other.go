//go:build unix && !linux && !darwin

package acpagent

import "errors"

// peerUID has no source for a socket peer credential on this OS. Sharing a
// session refuses rather than answers an unverified peer.
func peerCredentialUID(int) (int, error) {
	return 0, errors.New("checking the socket's owner is unsupported on this OS")
}
