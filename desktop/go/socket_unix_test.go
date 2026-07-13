//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// os.MkdirAll's mode applies only when it CREATES the directory; on an existing
// one it is a silent no-op that neither chmods nor checks ownership. The dev
// socket path is predictable, so another local user can pre-create it
// world-writable, then unlink our socket and bind their own -- and the sidecar RPC
// has no admission control, so whoever answers on it owns the session.
func TestPrepareEndpoint_TightensPreCreatedWorldWritableDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "leapmux-desktop")
	require.NoError(t, os.MkdirAll(dir, 0o777))
	// Defeat the umask: reproduce the squatted directory exactly.
	require.NoError(t, os.Chmod(dir, 0o777))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o777), info.Mode().Perm(), "precondition: the dir starts world-writable")

	endpoint, err := prepareEndpoint(filepath.Join(dir, "sidecar.sock"))
	require.NoError(t, err)
	assert.Contains(t, endpoint, "sidecar.sock")

	info, err = os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(),
		"an existing socket dir must be tightened, not trusted as-is")
}

// A freshly created directory must also end up private.
func TestPrepareEndpoint_CreatesPrivateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "leapmux-desktop")

	_, err := prepareEndpoint(filepath.Join(dir, "sidecar.sock"))
	require.NoError(t, err)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// A path whose parent is a FILE must be refused rather than silently used.
func TestPrepareEndpoint_RejectsNonDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))

	_, err := prepareEndpoint(filepath.Join(file, "sidecar.sock"))
	require.Error(t, err)
}

// A socket dir that is a SYMLINK must be refused, not followed.
//
// The dev socket path is predictable, so a local attacker can pre-create it as a
// symlink to a directory we already own (/tmp's sticky bit stops them unlinking our
// entry, not creating a name that does not exist yet). A path-based os.Stat resolves
// the link and validates the TARGET: the uid check passes (we own it) and the mode
// check then chmods it -- so the guard that exists to refuse a squatted directory
// instead becomes a chmod-0700 primitive on any directory of ours the attacker
// names, and the sidecar binds its unauthenticated control socket where they chose.
func TestPrepareEndpoint_RefusesSymlinkedSocketDir(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "attacker-chosen")
	require.NoError(t, os.Mkdir(target, 0o750)) // group bit: the old code would chmod this to 0700
	link := filepath.Join(root, "leapmux-desktop")
	require.NoError(t, os.Symlink(target, link))

	_, err := prepareEndpoint(filepath.Join(link, "sidecar.sock"))
	require.Error(t, err, "a symlinked socket dir must be refused, not followed")

	info, statErr := os.Stat(target)
	require.NoError(t, statErr)
	assert.Equal(t, os.FileMode(0o750), info.Mode().Perm(),
		"the symlink target must not be chmod'ed through the link")
}

// A symlink whose target does not exist must also be refused. This one is already
// caught upstream -- os.MkdirAll's Mkdir hits EEXIST on the link and its Lstat
// fallback sees a non-directory -- so it passes with or without the O_NOFOLLOW
// guard; it is here to pin that the dangling squat stays refused if the MkdirAll
// step is ever reordered or replaced.
func TestPrepareEndpoint_RefusesDanglingSymlinkedSocketDir(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "leapmux-desktop")
	require.NoError(t, os.Symlink(filepath.Join(root, "does-not-exist"), link))

	_, err := prepareEndpoint(filepath.Join(link, "sidecar.sock"))
	require.Error(t, err, "a dangling symlinked socket dir must be refused")
}
