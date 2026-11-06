//go:build unix

package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// syscallStatT is the platform stat record the ownership checks read.
type syscallStatT = syscall.Stat_t

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

// preparePrivateDir creates dir when missing and verifies it is private
// to this account: mode 0700 and this account's owner. A directory
// another account owns, or one wider than the account, is a refusal
// before anything inside it is modified.
func preparePrivateDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		return verifyPrivateDir(dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("lifecycle: %s exists and is not a directory", dir)
	}
	if stat, ok := info.Sys().(*syscallStatT); ok && uint32(stat.Uid) != uint32(os.Geteuid()) {
		return fmt.Errorf("lifecycle: %s belongs to another account (uid %d)", dir, stat.Uid)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("lifecycle: %s is wider than this account (mode %s); tighten it before use", dir, info.Mode().Perm())
	}
	return nil
}

// verifyPrivateDir re-reads a directory it just created, so a umask
// surprise fails loudly rather than widening the lifecycle area.
func verifyPrivateDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("lifecycle: %s was created wider than this account (mode %s)", dir, info.Mode().Perm())
	}
	return nil
}

// verifyPrivateFile rejects a record or token that is not a regular
// file, not owned by this account, or readable past the account.
func verifyPrivateFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("lifecycle: %s is not a regular file", path)
	}
	if stat, ok := info.Sys().(*syscallStatT); ok && uint32(stat.Uid) != uint32(os.Geteuid()) {
		return fmt.Errorf("lifecycle: %s belongs to another account (uid %d)", path, stat.Uid)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("lifecycle: %s is wider than this account (mode %s)", path, info.Mode().Perm())
	}
	return nil
}

// maxUnixSocketPath is the conservative sockaddr_un limit across the
// supported Unix kernels.
const maxUnixSocketPath = 100
