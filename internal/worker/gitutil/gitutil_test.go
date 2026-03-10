package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initRepo creates a temporary git repo with an initial commit and returns its path.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmds := [][]string{
		{"git", "-C", dir, "init"},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
		{"git", "-C", dir, "config", "user.name", "Test"},
		{"git", "-C", dir, "commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		require.NoError(t, cmd.Run(), "command failed: %v", args)
	}
	return dir
}

func TestGetOriginURL(t *testing.T) {
	t.Run("returns origin URL when set", func(t *testing.T) {
		dir := initRepo(t)
		expected := "https://github.com/example/repo.git"
		cmd := exec.Command("git", "-C", dir, "remote", "add", "origin", expected)
		require.NoError(t, cmd.Run())

		got := GetOriginURL(dir)
		assert.Equal(t, expected, got)
	})

	t.Run("returns SSH origin URL", func(t *testing.T) {
		dir := initRepo(t)
		expected := "git@github.com:example/repo.git"
		cmd := exec.Command("git", "-C", dir, "remote", "add", "origin", expected)
		require.NoError(t, cmd.Run())

		got := GetOriginURL(dir)
		assert.Equal(t, expected, got)
	})

	t.Run("returns empty string when no origin", func(t *testing.T) {
		dir := initRepo(t)
		got := GetOriginURL(dir)
		assert.Empty(t, got)
	})

	t.Run("returns empty string for non-git directory", func(t *testing.T) {
		dir := t.TempDir()
		got := GetOriginURL(dir)
		assert.Empty(t, got)
	})

	t.Run("returns empty string for nonexistent directory", func(t *testing.T) {
		got := GetOriginURL(filepath.Join(t.TempDir(), "nonexistent"))
		assert.Empty(t, got)
	})
}

func TestGetGitStatus_OriginURL(t *testing.T) {
	t.Run("includes origin URL in status", func(t *testing.T) {
		dir := initRepo(t)
		expected := "https://github.com/example/repo.git"
		cmd := exec.Command("git", "-C", dir, "remote", "add", "origin", expected)
		require.NoError(t, cmd.Run())

		status := GetGitStatus(dir)
		require.NotNil(t, status)
		assert.Equal(t, expected, status.OriginURL)
	})

	t.Run("origin URL is empty when no remote", func(t *testing.T) {
		dir := initRepo(t)

		status := GetGitStatus(dir)
		require.NotNil(t, status)
		assert.Empty(t, status.OriginURL)
	})
}

func TestGetGitStatus_Branch(t *testing.T) {
	dir := initRepo(t)
	status := GetGitStatus(dir)
	require.NotNil(t, status)
	assert.NotEmpty(t, status.Branch)
}

func TestGetGitStatus_Modified(t *testing.T) {
	dir := initRepo(t)

	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("hello"), 0o644))
	cmd := exec.Command("git", "-C", dir, "add", "test.txt")
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "-C", dir, "commit", "-m", "add test.txt")
	require.NoError(t, cmd.Run())

	require.NoError(t, os.WriteFile(filePath, []byte("world"), 0o644))

	status := GetGitStatus(dir)
	require.NotNil(t, status)
	assert.True(t, status.Modified)
}

func TestGetGitStatus_Untracked(t *testing.T) {
	dir := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("new"), 0o644))

	status := GetGitStatus(dir)
	require.NotNil(t, status)
	assert.True(t, status.Untracked)
}

func TestGetGitStatus_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	status := GetGitStatus(dir)
	assert.Nil(t, status)
}

func TestGetPerFileStatus_UntrackedLines(t *testing.T) {
	dir := initRepo(t)

	// Create an untracked file with 3 lines.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("a\nb\nc\n"), 0o644))

	files, err := GetPerFileStatus(dir)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "new.txt", files[0].Path)
	assert.Equal(t, byte('?'), files[0].UnstagedStatus)
	// Untracked files don't have line counts (numstat doesn't cover them).
	assert.Equal(t, 0, files[0].LinesAdded)
	assert.Equal(t, 0, files[0].LinesDeleted)
}

func TestGetPerFileStatus_UntrackedBinaryFile(t *testing.T) {
	dir := initRepo(t)

	// Create an untracked binary file.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "image.bin"), []byte("\x89PNG\r\n\x00\x00"), 0o644))

	files, err := GetPerFileStatus(dir)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "image.bin", files[0].Path)
	assert.Equal(t, 0, files[0].LinesAdded)
}

func TestGetPerFileStatus_MixedTrackedAndUntracked(t *testing.T) {
	dir := initRepo(t)

	// Create a committed file, then modify it (unstaged).
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("line1\n"), 0o644))
	run(t, dir, "git", "add", "tracked.txt")
	run(t, dir, "git", "commit", "-m", "add tracked")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("line1\nline2\n"), 0o644))

	// Create an untracked text file.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("a\nb\nc\nd\n"), 0o644))

	// Create an untracked binary file.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "binary.dat"), []byte("data\x00bin\n"), 0o644))

	files, err := GetPerFileStatus(dir)
	require.NoError(t, err)
	require.Len(t, files, 3)

	// Build a map for easier assertion.
	byPath := make(map[string]FileStatus, len(files))
	for _, f := range files {
		byPath[f.Path] = f
	}

	// tracked.txt: unstaged modification, 1 line added via numstat.
	tracked := byPath["tracked.txt"]
	assert.Equal(t, byte('M'), tracked.UnstagedStatus)
	assert.Equal(t, 1, tracked.LinesAdded)
	assert.Equal(t, 0, tracked.LinesDeleted)

	// untracked.txt: untracked, no line counts (numstat doesn't cover them).
	untracked := byPath["untracked.txt"]
	assert.Equal(t, byte('?'), untracked.UnstagedStatus)
	assert.Equal(t, 0, untracked.LinesAdded)
	assert.Equal(t, 0, untracked.LinesDeleted)

	// binary.dat: untracked binary, 0 lines.
	binary := byPath["binary.dat"]
	assert.Equal(t, byte('?'), binary.UnstagedStatus)
	assert.Equal(t, 0, binary.LinesAdded)
}

// run executes a command in the given directory.
func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	require.NoError(t, cmd.Run(), "command failed: %s %v", name, args)
}
