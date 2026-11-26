//go:build windows

package locallisten

import (
	"fmt"
	"os/user"
)

const defaultHubPipePrefix = "leapmux-hub-"

// DefaultHubAddress returns the default local inter-process communication (IPC)
// URL for a Hub on Windows: a per-user named pipe whose security identifier
// (SID) prevents cross-user collisions on a shared host.
func DefaultHubAddress(_ string) (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolve current user for local-listen default: %w", err)
	}
	return defaultHubAddressForUser(u.Uid)
}

func defaultHubAddressForUser(uid string) (string, error) {
	if uid == "" {
		return "", fmt.Errorf("current user has empty SID; cannot derive local-listen default")
	}
	return string(SchemeNpipe) + ":" + defaultHubPipePrefix + uid, nil
}
