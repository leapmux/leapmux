//go:build windows

package filelock

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A lock path that is a reparse point, or any non-regular object, is
// rejected by Open itself, before the caller can lock or modify the
// target.
func TestOpenRejectsReparsePoint(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.bin")
	require.NoError(t, os.WriteFile(target, []byte("keep me untouched"), 0o600))
	link := filepath.Join(dir, "test.lock")
	require.NoError(t, os.Symlink(target, link))

	before, err := os.Stat(target)
	require.NoError(t, err)

	_, err = Open(link, false)
	require.Error(t, err, "a reparse-point lock path is refused")
	assert.Contains(t, err.Error(), "not a regular file")

	after, err := os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime(), "the target behind the link is untouched")
}
