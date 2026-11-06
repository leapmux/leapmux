//go:build unix

package filelock

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A lock path that links to an unrelated file is rejected by Open
// itself, before the caller can lock or modify the target: the target's
// bytes and times stay as they were, and no lock is taken on it.
func TestOpenRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.bin")
	require.NoError(t, os.WriteFile(target, []byte("keep me untouched"), 0o600))
	link := filepath.Join(dir, "test.lock")
	require.NoError(t, os.Symlink(target, link))

	before, err := os.Stat(target)
	require.NoError(t, err)

	_, err = Open(link, false)
	require.Error(t, err, "a symlinked lock path is refused")
	var errno syscall.Errno
	require.ErrorAs(t, err, &errno)
	assert.Equal(t, syscall.ELOOP, errno, "O_NOFOLLOW reports the link itself")

	after, err := os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime(), "the target behind the link is untouched")
	assert.Equal(t, "keep me untouched", string(mustRead(t, target)))
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
