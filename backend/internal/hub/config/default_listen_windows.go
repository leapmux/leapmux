//go:build windows

package config

import (
	"fmt"
	"os/user"
)

// defaultLocalListen returns a per-user npipe URL; the SID in the name
// prevents cross-user collisions on a shared host.
func defaultLocalListen(_ string) (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolve current user for local-listen default: %w", err)
	}
	if u.Uid == "" {
		return "", fmt.Errorf("current user has empty SID; cannot derive local-listen default")
	}
	return "npipe:leapmux-hub-" + u.Uid, nil
}
