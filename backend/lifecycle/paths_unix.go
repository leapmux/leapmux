//go:build unix

package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
)

// runtimeDir returns the short, account-owned directory Unix sockets
// live in. The default sits directly under the system temporary
// directory, one directory per account, so a deep data directory cannot
// push a socket path past the kernel's sockaddr_un limit.
func runtimeDir() (string, error) {
	if fromEnv := os.Getenv("LEAPMUX_RUNTIME_DIR"); fromEnv != "" {
		return filepath.Clean(fromEnv), nil
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("leapmux-%d", os.Getuid())), nil
}

// controlURLFor derives the control endpoint URL from the instance
// identity alone.
func controlURLFor(id InstanceID) string {
	dir, err := runtimeDir()
	if err != nil {
		// A clean of an environment variable cannot fail here; the bind
		// below surfaces the unusable path.
		return "unix:" + filepath.Join(os.TempDir(), fmt.Sprintf("leapmux-%d", os.Getuid()), string(id)+".sock")
	}
	return "unix:" + filepath.Join(dir, string(id)+".sock")
}
