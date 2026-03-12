package filebrowser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListDirectory(t *testing.T) {
	dir := t.TempDir()

	// Create some files and dirs.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("hello"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file2.txt"), []byte("world"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir"), 0o755))

	absPath, entries, err := ListDirectory(dir, 0)
	require.NoError(t, err, "ListDirectory")

	assert.Equal(t, dir, absPath)
	require.Len(t, entries, 3)

	// Check that we have the expected names.
	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
	}
	for _, name := range []string{"file1.txt", "file2.txt", "subdir"} {
		assert.True(t, names[name], "missing entry: %s", name)
	}
}

func TestListDirectory_NotExists(t *testing.T) {
	_, _, err := ListDirectory("/nonexistent/path/xyz", 0)
	assert.Error(t, err, "expected error for nonexistent path")
}

func TestListDirectory_MergeSingleChild(t *testing.T) {
	dir := t.TempDir()

	// Create a chain: dir/a/b/c/ with a file in c
	chain := filepath.Join(dir, "a", "b", "c")
	require.NoError(t, os.MkdirAll(chain, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(chain, "file.txt"), []byte("hello"), 0o644))

	// Without merging (maxDepth=0), should return just "a"
	_, entries, err := ListDirectory(dir, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "a", entries[0].Name)
	assert.True(t, entries[0].IsDir)

	// With merging (maxDepth=5), should return "a/b/c"
	_, entries, err = ListDirectory(dir, 5)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "a/b/c", entries[0].Name)
	assert.True(t, entries[0].IsDir)
}

func TestListDirectory_MergeStopsAtMultipleChildren(t *testing.T) {
	dir := t.TempDir()

	// Create: dir/a/b1/ and dir/a/b2/
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "a", "b1"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "a", "b2"), 0o755))

	// Merging should stop at "a" since it has two children
	_, entries, err := ListDirectory(dir, 5)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "a", entries[0].Name)
}

func TestListDirectory_MergeRespectsMaxDepth(t *testing.T) {
	dir := t.TempDir()

	// Create a deep chain: dir/a/b/c/d/e/f/
	chain := filepath.Join(dir, "a", "b", "c", "d", "e", "f")
	require.NoError(t, os.MkdirAll(chain, 0o755))

	// With maxDepth=2, should merge up to 2 levels: "a/b/c"
	_, entries, err := ListDirectory(dir, 2)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "a/b/c", entries[0].Name)
}

func TestListDirectory_MergeIgnoresFiles(t *testing.T) {
	dir := t.TempDir()

	// Create: dir/a/ with a file (not a directory) as single child
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "a"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a", "file.txt"), []byte("hi"), 0o644))

	// Merging should NOT merge file children — "a" stays as "a"
	_, entries, err := ListDirectory(dir, 5)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "a", entries[0].Name)
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	content := "Hello, World! This is test content."
	require.NoError(t, os.WriteFile(filePath, []byte(content), 0o644))

	// Read full file.
	absPath, data, totalSize, err := ReadFile(filePath, 0, 0)
	require.NoError(t, err, "ReadFile")
	assert.Equal(t, filePath, absPath)
	assert.Equal(t, content, string(data))
	assert.Equal(t, int64(len(content)), totalSize)
}

func TestReadFile_WithOffsetAndLimit(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("0123456789"), 0o644))

	_, data, _, err := ReadFile(filePath, 3, 4)
	require.NoError(t, err, "ReadFile")
	assert.Equal(t, "3456", string(data))
}

func TestReadFile_Directory(t *testing.T) {
	dir := t.TempDir()
	_, _, _, err := ReadFile(dir, 0, 0)
	assert.Error(t, err, "expected error for directory")
}

func TestStatFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "stat.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("content"), 0o644))

	absPath, entry, err := StatFile(filePath)
	require.NoError(t, err, "StatFile")
	assert.Equal(t, filePath, absPath)
	assert.Equal(t, "stat.txt", entry.Name)
	assert.False(t, entry.IsDir, "expected IsDir = false")
	assert.Equal(t, int64(7), entry.Size)
}

func TestStatFile_Directory(t *testing.T) {
	dir := t.TempDir()
	_, entry, err := StatFile(dir)
	require.NoError(t, err, "StatFile")
	assert.True(t, entry.IsDir, "expected IsDir = true")
}

func TestReadFile_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "empty.txt")
	require.NoError(t, os.WriteFile(filePath, []byte{}, 0o644))

	absPath, data, totalSize, err := ReadFile(filePath, 0, 0)
	require.NoError(t, err, "ReadFile on empty file")
	assert.Equal(t, filePath, absPath)
	assert.Empty(t, data, "data should be empty for an empty file")
	assert.Equal(t, int64(0), totalSize)
}

func TestStatFile_NonExistent(t *testing.T) {
	_, _, err := StatFile("/nonexistent/path/xyz/no-such-file.txt")
	assert.Error(t, err, "expected error for non-existent path")
}

func TestTildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err, "UserHomeDir")

	// ~ alone should resolve to home directory via StatFile.
	absPath, _, err := StatFile("~")
	require.NoError(t, err, "StatFile(~)")
	assert.Equal(t, home, absPath)

	// ~/Documents should resolve to home + Documents.
	// The directory may not exist, so just check that the error is NOT "invalid path".
	absPath, _, err = StatFile("~/Documents")
	if err != nil {
		assert.NotContains(t, err.Error(), "invalid path", "tilde should be expanded, not rejected")
	} else {
		assert.Equal(t, filepath.Join(home, "Documents"), absPath)
	}
}

func TestEmptyPath(t *testing.T) {
	_, _, err := StatFile("")
	assert.Error(t, err, "expected error for empty path")
	assert.Contains(t, err.Error(), "invalid path")
}

func TestPathTraversal_Rejected(t *testing.T) {
	_, _, err := StatFile("/tmp/../etc/passwd")
	assert.Error(t, err, "expected error for path traversal")
	assert.Contains(t, err.Error(), "invalid path")
}
