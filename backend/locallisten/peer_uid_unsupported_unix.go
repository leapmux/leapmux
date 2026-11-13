//go:build unix && !linux && !darwin

package locallisten

import "fmt"

func unixPeerCredentialsAvailable() error {
	return fmt.Errorf("%w: Unix listeners require native peer-credential verification on this platform", ErrUnsupportedScheme)
}
func socketPeerUID(uintptr) (uint32, error) { return 0, unixPeerCredentialsAvailable() }
