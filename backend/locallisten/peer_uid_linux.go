//go:build linux

package locallisten

import "golang.org/x/sys/unix"

func unixPeerCredentialsAvailable() error { return nil }
func socketPeerUID(fd uintptr) (uint32, error) {
	credentials, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	return credentials.Uid, nil
}
