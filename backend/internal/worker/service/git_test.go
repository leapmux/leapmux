package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

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

// run executes a command in the given directory.
func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	require.NoError(t, cmd.Run(), "command failed: %s %v", name, args)
}

func TestGetGitFileStatusEntries_UntrackedFile(t *testing.T) {
	dir := initRepo(t)

	// Create an untracked file with 3 lines.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("a\nb\nc\n"), 0o644))

	files, err := getGitFileStatusEntries(context.Background(), dir)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "new.txt", files[0].Path)
	assert.Equal(t, leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNTRACKED, files[0].UnstagedStatus)
	// Untracked files don't have line counts (numstat doesn't cover them).
	assert.Equal(t, int32(0), files[0].LinesAdded)
	assert.Equal(t, int32(0), files[0].LinesDeleted)
}

func TestGetGitFileStatusEntries_UntrackedBinaryFile(t *testing.T) {
	dir := initRepo(t)

	// Create an untracked binary file.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "image.bin"), []byte("\x89PNG\r\n\x00\x00"), 0o644))

	files, err := getGitFileStatusEntries(context.Background(), dir)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "image.bin", files[0].Path)
	assert.Equal(t, int32(0), files[0].LinesAdded)
}

func TestGetGitFileStatusEntries_MixedTrackedAndUntracked(t *testing.T) {
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

	files, err := getGitFileStatusEntries(context.Background(), dir)
	require.NoError(t, err)
	require.Len(t, files, 3)

	// Build a map for easier assertion.
	byPath := make(map[string]*leapmuxv1.GitFileStatusEntry, len(files))
	for _, f := range files {
		byPath[f.Path] = f
	}

	// tracked.txt: unstaged modification, 1 line added via numstat.
	tracked := byPath["tracked.txt"]
	assert.Equal(t, leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_MODIFIED, tracked.UnstagedStatus)
	assert.Equal(t, int32(1), tracked.LinesAdded)
	assert.Equal(t, int32(0), tracked.LinesDeleted)

	// untracked.txt: untracked, no line counts (numstat doesn't cover them).
	untracked := byPath["untracked.txt"]
	assert.Equal(t, leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNTRACKED, untracked.UnstagedStatus)
	assert.Equal(t, int32(0), untracked.LinesAdded)
	assert.Equal(t, int32(0), untracked.LinesDeleted)

	// binary.dat: untracked binary, 0 lines.
	binary := byPath["binary.dat"]
	assert.Equal(t, leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNTRACKED, binary.UnstagedStatus)
	assert.Equal(t, int32(0), binary.LinesAdded)
}

func TestGetGitFileStatusEntries_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	files, err := getGitFileStatusEntries(context.Background(), dir)
	require.NoError(t, err)
	assert.Nil(t, files)
}

func TestResolveMainRepoRoot_RegularRepo(t *testing.T) {
	dir := initRepo(t)
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	root, err := resolveMainRepoRoot(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, resolved, root)
}

func TestResolveMainRepoRoot_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	_, err := resolveMainRepoRoot(context.Background(), dir)
	assert.ErrorIs(t, err, errNotGitRepo)
}

func TestResolveMainRepoRoot_Subdirectory(t *testing.T) {
	dir := initRepo(t)
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	subDir := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(subDir, 0o755))

	root, err := resolveMainRepoRoot(context.Background(), subDir)
	require.NoError(t, err)
	assert.Equal(t, resolved, root)
}

func TestResolveMainRepoRoot_LinkedWorktree(t *testing.T) {
	dir := initRepo(t)
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	// Create a linked worktree.
	wtDir := filepath.Join(t.TempDir(), "my-worktree")
	run(t, dir, "git", "worktree", "add", "-b", "wt-branch", wtDir)

	// resolveMainRepoRoot from the worktree should return the main repo root.
	root, err := resolveMainRepoRoot(context.Background(), wtDir)
	require.NoError(t, err)
	assert.Equal(t, resolved, root)
}

func TestResolveMainRepoRoot_NestedWorktree(t *testing.T) {
	dir := initRepo(t)
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	// Create a worktree, then resolve from a subdirectory within it.
	wtDir := filepath.Join(t.TempDir(), "nested-wt")
	run(t, dir, "git", "worktree", "add", "-b", "nested-branch", wtDir)

	subDir := filepath.Join(wtDir, "deep")
	require.NoError(t, os.MkdirAll(subDir, 0o755))

	root, err := resolveMainRepoRoot(context.Background(), subDir)
	require.NoError(t, err)
	assert.Equal(t, resolved, root)
}
