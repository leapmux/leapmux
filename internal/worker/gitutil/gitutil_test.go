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
