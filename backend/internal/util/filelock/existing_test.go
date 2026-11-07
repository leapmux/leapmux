package filelock

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenExistingNeverCreatesAndStillLocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	_, err := OpenExisting(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.NoFileExists(t, path)
	first, err := Open(path, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	held, err := TryLock(first)
	require.NoError(t, err)
	require.True(t, held)
	second, err := OpenExisting(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	held, err = TryLock(second)
	require.NoError(t, err)
	assert.False(t, held)
	require.NoError(t, Unlock(first))
	held, err = TryLock(second)
	require.NoError(t, err)
	assert.True(t, held)
	require.NoError(t, Unlock(second))
}
