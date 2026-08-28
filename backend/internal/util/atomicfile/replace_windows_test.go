//go:build windows

package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestDestCannotReplace_Directory(t *testing.T) {
	t.Parallel()
	assert.True(t, destCannotReplace(t.TempDir()),
		"a directory at the destination can never succeed, so it must not be retried")
}

func TestDestCannotReplace_ReadOnlyFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(path, []byte("one"), 0o600))
	require.NoError(t, os.Chmod(path, 0o400))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	assert.True(t, destCannotReplace(path),
		"a read-only destination is the same ACCESS_DENIED as a sharing conflict, and must not be retried")
}

func TestDestCannotReplace_WritableFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(path, []byte("one"), 0o600))
	assert.False(t, destCannotReplace(path),
		"a sharing conflict on a writable file is transient and must be retried")
}

func TestDestCannotReplace_MissingPath(t *testing.T) {
	t.Parallel()
	assert.False(t, destCannotReplace(filepath.Join(t.TempDir(), "absent")),
		"a missing destination is not a permanent refusal")
}

func TestIsSharingConflict_RecognizesTheWindowsReplaceErrors(t *testing.T) {
	t.Parallel()

	assert.True(t, isSharingConflict(&os.LinkError{Err: windows.ERROR_ACCESS_DENIED}))
	assert.True(t, isSharingConflict(&os.LinkError{Err: windows.ERROR_SHARING_VIOLATION}))
	assert.True(t, isSharingConflict(&os.LinkError{Err: windows.ERROR_LOCK_VIOLATION}))
	assert.True(t, isSharingConflict(windows.ERROR_ACCESS_DENIED),
		"os.Rename wraps this in a LinkError, and a classifier that only matches the wrapper misses the errno itself")
	assert.True(t, isSharingConflict(fmt.Errorf("rename: %w", &os.LinkError{Err: windows.ERROR_ACCESS_DENIED})))
	assert.False(t, isSharingConflict(nil))
	assert.False(t, isSharingConflict(os.ErrNotExist))
	assert.False(t, isSharingConflict(&os.LinkError{Err: windows.ERROR_ALREADY_EXISTS}))
}

func TestWriteFile_DirectoryReadOnlyDoesNotBlockCreate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	require.NoError(t, WriteFile(path, []byte("one"), 0o600),
		"Windows ignores a directory's read-only bit for child creates; chmod 0500 on the config dir cannot stand in for a failed save")
	require.NoError(t, WriteFile(path, []byte("two"), 0o600))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "two", string(data))
}

func TestWriteFile_ExclusiveHandleBlocksReplaceUntilClosed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	require.NoError(t, WriteFile(path, []byte("one"), 0o600))

	h := openExclusive(t, path)
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = windows.CloseHandle(h)
		}
	})
	assert.False(t, destCannotReplace(path),
		"an exclusive handle is a sharing conflict, not a permanent refusal")
	require.Error(t, WriteFile(path, []byte("two"), 0o600),
		"Windows cannot replace a destination that still has an exclusive handle")

	require.NoError(t, windows.CloseHandle(h))
	closed = true

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "one", string(data), "the replace must not land while the exclusive handle is held")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the exhausted retry must not leave a temporary file")
	assert.Equal(t, "creds.json", entries[0].Name())

	require.NoError(t, WriteFile(path, []byte("two"), 0o600))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "two", string(data))
}

func openExclusive(t *testing.T, path string) windows.Handle {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	require.NoError(t, err)
	h, err := windows.CreateFile(
		name,
		windows.GENERIC_READ,
		0,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	require.NoError(t, err)
	return h
}
