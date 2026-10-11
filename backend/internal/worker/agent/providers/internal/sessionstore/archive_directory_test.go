package sessionstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type checkedArchiveTestRoot struct {
	ArchiveRoot
	afterLstat func(string)
	opened     []ArchiveRoot
}

func (r *checkedArchiveTestRoot) Lstat(name string) (os.FileInfo, error) {
	info, err := r.ArchiveRoot.Lstat(name)
	if err == nil && r.afterLstat != nil {
		r.afterLstat(name)
	}
	return info, err
}

func (r *checkedArchiveTestRoot) OpenChild(name string) (ArchiveRoot, error) {
	child, err := r.ArchiveRoot.OpenChild(name)
	if err == nil {
		r.opened = append(r.opened, child)
	}
	return child, err
}

func TestReadRegularFileWithoutSymlinkAncestorsReadsAndClosesDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "one", "two"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "one", "two", "record.json"), []byte("valid"), 0o600))
	opened, err := OpenArchiveRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	root := &checkedArchiveTestRoot{ArchiveRoot: opened}

	data, err := ReadRegularFileWithoutSymlinkAncestors(root, 5, "one", "two", "record.json")
	require.NoError(t, err)
	assert.Equal(t, []byte("valid"), data)
	require.Len(t, root.opened, 1, "the first anchored directory belongs to the instrumented root")
	_, err = root.opened[0].Stat(".")
	require.ErrorIs(t, err, os.ErrClosed)
}

func TestReadRegularFileWithoutSymlinkAncestorsRejectsInRootSwap(t *testing.T) {
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects")
	require.NoError(t, os.MkdirAll(projects, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(projects, "child.json"), []byte("safe"), 0o600))
	alternate := filepath.Join(dir, "alternate")
	require.NoError(t, os.MkdirAll(alternate, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(alternate, "child.json"), []byte("substitute"), 0o600))
	swapped := false
	opened, err := OpenArchiveRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	root := &checkedArchiveTestRoot{ArchiveRoot: opened}
	root.afterLstat = func(name string) {
		if name != "projects" || swapped {
			return
		}
		swapped = true
		require.NoError(t, os.Rename(projects, projects+"-held"))
		if linkErr := os.Symlink("alternate", projects); linkErr != nil {
			t.Skipf("the test filesystem cannot create a symlink: %v", linkErr)
		}
	}

	data, err := ReadRegularFileWithoutSymlinkAncestors(root, 20, "projects", "child.json")
	require.True(t, swapped)
	require.ErrorContains(t, err, "changed before it opened")
	assert.Empty(t, data)
	require.Len(t, root.opened, 1)
	_, err = root.opened[0].Stat(".")
	require.ErrorIs(t, err, os.ErrClosed)
}

func TestReadRegularFileWithoutSymlinkAncestorsRejectsUnsafePaths(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "real"), 0o700))
	if err := os.Symlink("real", filepath.Join(dir, "alias")); err != nil {
		t.Skipf("the test filesystem cannot create a symlink: %v", err)
	}
	root, err := OpenArchiveRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	for _, parts := range [][]string{nil, {""}, {"..", "file"}, {"real/file"}, {`real\file`}} {
		data, err := ReadRegularFileWithoutSymlinkAncestors(root, 10, parts...)
		require.ErrorContains(t, err, "component is invalid")
		assert.Empty(t, data)
	}
	data, err := ReadRegularFileWithoutSymlinkAncestors(root, 10, "alias", "file")
	require.ErrorContains(t, err, "symlink")
	assert.Empty(t, data)
	data, err = ReadRegularFileWithoutSymlinkAncestors(root, 10, "missing", "file")
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Empty(t, data)
	data, err = ReadRegularFileWithoutSymlinkAncestors(root, 10, "real", "missing")
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Empty(t, data)
}
