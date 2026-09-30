package sessionstore

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type archiveFileTestRoot struct {
	*os.Root
	open func(string) (*os.File, error)
}

func (r archiveFileTestRoot) Open(name string) (*os.File, error) {
	if r.open != nil {
		return r.open(name)
	}
	return r.Root.Open(name)
}

func openArchiveFileTestRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	return root
}

func TestReadRegularFileAcceptsExactLimitAndEmptyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "full.json"), []byte("valid"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "empty.json"), nil, 0o600))
	root := openArchiveFileTestRoot(t, dir)

	full, err := ReadRegularFile(root, "full.json", 5)
	require.NoError(t, err)
	assert.Equal(t, []byte("valid"), full)
	empty, err := ReadRegularFile(root, "empty.json", 0)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestReadRegularFileRejectsInvalidLimitAndFinalType(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.json"), []byte("data"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "directory"), 0o700))
	root := openArchiveFileTestRoot(t, dir)

	for _, limit := range []int64{-1, math.MaxInt64} {
		data, err := ReadRegularFile(root, "file.json", limit)
		require.Error(t, err)
		assert.Empty(t, data)
	}
	data, err := ReadRegularFile(root, "file.json", 3)
	require.ErrorContains(t, err, "not a regular file within 3 bytes")
	require.ErrorIs(t, err, ErrArchiveFileModeOrSize)
	assert.Empty(t, data)
	data, err = ReadRegularFile(root, "directory", 100)
	require.ErrorIs(t, err, ErrArchiveFileModeOrSize)
	assert.Empty(t, data)
	data, err = ReadRegularFile(root, "missing.json", 100)
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Empty(t, data)
	t.Run("final symlink", func(t *testing.T) {
		if err := os.Symlink("file.json", filepath.Join(dir, "link.json")); err != nil {
			t.Skipf("the test filesystem cannot create a symlink: %v", err)
		}
		linkData, linkErr := ReadRegularFile(root, "link.json", 100)
		require.ErrorContains(t, linkErr, "symlink")
		assert.Empty(t, linkData)
	})
}

func TestReadRegularFileRejectsCheckedOpenSwapAndGrowth(t *testing.T) {
	for _, tc := range []struct {
		name    string
		symlink bool
		grow    bool
	}{
		{name: "regular replacement"},
		{name: "symlink replacement", symlink: true},
		{name: "file growth", grow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "archive.json")
			require.NoError(t, os.WriteFile(path, []byte("safe"), 0o600))
			root := openArchiveFileTestRoot(t, dir)
			swapped := false
			reader := archiveFileTestRoot{Root: root, open: func(name string) (*os.File, error) {
				swapped = true
				if tc.grow {
					require.NoError(t, os.WriteFile(path, []byte("unsafe"), 0o600))
				} else {
					require.NoError(t, os.Rename(path, path+".prior"))
					if tc.symlink {
						outside := filepath.Join(t.TempDir(), "outside.json")
						require.NoError(t, os.WriteFile(outside, []byte("unsafe"), 0o600))
						if err := os.Symlink(outside, path); err != nil {
							t.Skipf("the test filesystem cannot create a symlink: %v", err)
						}
					} else {
						require.NoError(t, os.WriteFile(path, []byte("evil"), 0o600))
					}
				}
				return root.Open(name)
			}}
			data, err := ReadRegularFile(reader, "archive.json", 4)
			assert.True(t, swapped)
			require.Error(t, err)
			assert.Empty(t, data)
		})
	}
}

func TestReadRegularFileReturnsOpenAndStatErrorsWithoutData(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "archive.json"), []byte("safe"), 0o600))
	root := openArchiveFileTestRoot(t, dir)
	openError := errors.New("injected open failure")
	data, err := ReadRegularFile(archiveFileTestRoot{Root: root, open: func(string) (*os.File, error) {
		return nil, openError
	}}, "archive.json", 4)
	require.ErrorIs(t, err, openError)
	assert.Empty(t, data)

	data, err = ReadRegularFile(archiveFileTestRoot{Root: root, open: func(name string) (*os.File, error) {
		file, openErr := root.Open(name)
		if openErr != nil {
			return nil, openErr
		}
		require.NoError(t, file.Close())
		return file, nil
	}}, "archive.json", 4)
	require.Error(t, err)
	assert.Empty(t, data)
}

func TestReadRegularFileJoinsStatAndCloseErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "archive.json"), []byte("safe"), 0o600))
	root := openArchiveFileTestRoot(t, dir)
	reader := archiveFileTestRoot{Root: root, open: func(name string) (*os.File, error) {
		file, err := root.Open(name)
		require.NoError(t, err)
		require.NoError(t, file.Close())
		return file, nil
	}}

	data, err := ReadRegularFile(reader, "archive.json", 4)
	assert.Empty(t, data)
	var joined interface{ Unwrap() []error }
	require.ErrorAs(t, err, &joined)
	require.Len(t, joined.Unwrap(), 2)
	for _, cause := range joined.Unwrap() {
		require.ErrorIs(t, cause, os.ErrClosed)
	}
}
