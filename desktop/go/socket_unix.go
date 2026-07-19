//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// prepareEndpoint creates the dev socket's parent directory and refuses to use one
// another local user could tamper with.
//
// os.MkdirAll's mode applies ONLY when it creates the directory: on an existing one
// it is a silent no-op that neither chmods nor checks ownership. That matters
// because the dev socket path is predictable (the shell derives it from
// std::env::temp_dir(), i.e. /tmp/leapmux-desktop on Linux), so another local user
// can pre-create that directory world-writable and wait. Owning a non-sticky
// directory lets them unlink our socket and bind their own -- and the sidecar RPC
// has no admission control by design, so whoever answers on it has full control of
// this user's session: mode switches, tunnels, and an HTTP proxy carrying their Hub
// cookies.
//
// So verify what we actually got rather than what we asked for: the directory must
// be ours, and must not be writable by group or other. Tightening our own directory
// is fine; one owned by somebody else is refused outright, since chmod would fail
// anyway and using it would mean trusting a squatter.
func prepareEndpoint(socketPath string) (string, error) {
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := requirePrivateDir(dir); err != nil {
		return "", err
	}
	return "unix:" + socketPath, nil
}

// requirePrivateDir enforces that dir is a real directory owned by this user and
// inaccessible to group/other, tightening the mode when it is ours to tighten.
//
// It validates the OPENED OBJECT, never a path string, because the path is the very
// thing under attack. Two properties follow, and both are load-bearing:
//
//   - O_NOFOLLOW refuses a symlink AT dir instead of resolving it. A path-based
//     os.Stat follows the link and reports on the TARGET, so a squatter who
//     pre-creates the predictable /tmp/leapmux-desktop as a symlink to a directory
//     we already own passes both checks below -- and then the chmod lands on THEIR
//     chosen directory, which is a chmod-0700 primitive on anything we own (say
//     ~/.ssh). MkdirAll is no defence: it succeeds on a symlink to an existing dir.
//   - Every check runs against this one descriptor (f.Stat, f.Chmod -> fstat,
//     fchmod), so the name cannot be swapped between the check and the chmod.
//
// /tmp's sticky bit does not help here: it stops a squatter unlinking OUR entry, not
// creating a name that does not exist yet.
func requirePrivateDir(dir string) error {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return fmt.Errorf("open socket dir %s (a symlink or non-directory here is refused, not followed): %w", dir, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat socket dir %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("socket dir %s is not a directory", dir)
	}
	// Ownership first: a directory belonging to someone else can be swapped under us
	// regardless of what its mode says right now. A missing Stat_t means we cannot
	// establish the owner at all, so refuse rather than fall through to the weaker
	// mode-only check -- this gate exists to fail closed.
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("socket dir %s: cannot determine owning uid", dir)
	}
	if uint64(st.Uid) != uint64(os.Getuid()) {
		return fmt.Errorf("socket dir %s is owned by uid %d, not %d", dir, st.Uid, os.Getuid())
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		if err := f.Chmod(0o700); err != nil {
			return fmt.Errorf("restrict socket dir %s (mode %o): %w", dir, perm, err)
		}
	}
	return nil
}
