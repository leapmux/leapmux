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

	absPath, entries, err := ListDirectory(dir)
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
	_, _, err := ListDirectory("/nonexistent/path/xyz")
	assert.Error(t, err, "expected error for nonexistent path")
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

func TestSecurePath_TildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err, "UserHomeDir")

	// ~ alone should resolve to home directory.
	path, err := securePath("~")
	require.NoError(t, err, "securePath(~)")
	assert.Equal(t, home, path)

	// ~/subdir should resolve to home + subdir.
	path, err = securePath("~/Documents")
	require.NoError(t, err, "securePath(~/Documents)")
	want := filepath.Join(home, "Documents")
	assert.Equal(t, want, path)
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

func TestSecurePath_NullByte(t *testing.T) {
	_, err := securePath("/tmp/foo\x00bar")
	assert.Error(t, err, "expected error for null byte in path")
}

func TestSecurePath_Empty(t *testing.T) {
	_, err := securePath("")
	assert.Error(t, err, "expected error for empty path")
}

func TestSecurePath_Traversal(t *testing.T) {
	// The path gets cleaned but is still resolved to absolute.
	path, err := securePath("/tmp/../etc/passwd")
	require.NoError(t, err, "securePath")
	assert.Equal(t, "/etc/passwd", path)
}
