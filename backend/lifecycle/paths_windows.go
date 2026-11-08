//go:build windows

package lifecycle

import (
	"os"
	"path/filepath"
)

// runtimeDir returns the account-owned directory for runtime scratch
// state. Mode bits alone do not restrict inherited access control lists
// on Windows, so the directory is created with the account-only SDDL
// and its ownership verified.
func runtimeDir() (string, error) {
	if fromEnv := os.Getenv("LEAPMUX_RUNTIME_DIR"); fromEnv != "" {
		return filepath.Clean(fromEnv), nil
	}
	return accountRuntimePath(os.TempDir(), currentAccount()), nil
}

// controlURLFor derives the named-pipe control endpoint from the
// instance identity and the account. The account keeps two accounts'
// instances off one pipe name; the identity keeps two configurations of
// one account apart.
func controlURLFor(id InstanceID) string {
	return "npipe:leapmux-" + hashID(currentAccount()) + "-" + string(id)
}

// maxUnixSocketPath is unused on Windows; named pipes have no length
// limit of this shape.
const maxUnixSocketPath = 0
