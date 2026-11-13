//go:build darwin

package locallisten

import "golang.org/x/sys/unix"

func unixPeerCredentialsAvailable() error { return nil }
func socketPeerUID(fd uintptr) (uint32, error) {
	credentials, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, err
	}
	return credentials.Uid, nil
}
