package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestGetGitFileStatus_ReturnsOriginUrlAndCurrentBranch(t *testing.T) {
	dir := initRepo(t)
	ctx := context.Background()

	// Set a remote origin URL.
	run(t, dir, "git", "remote", "add", "origin", "https://github.com/test/repo.git")

	// Create a file so there's something in the status.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello\n"), 0o644))

	// Resolve repo root (matches what the handler does).
	repoRoot, err := gitOutput(ctx, dir, "rev-parse", "--show-toplevel")
	require.NoError(t, err)
	repoRoot = strings.TrimSpace(repoRoot)

	// Simulate what the handler does: get files, then branch/origin.
	files, err := getGitFileStatusEntries(ctx, repoRoot)
	require.NoError(t, err)
	require.NotEmpty(t, files)

	resp := &leapmuxv1.GetGitFileStatusResponse{
		RepoRoot: repoRoot,
		Files:    files,
	}
	if branch, err := gitOutput(ctx, repoRoot, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		resp.CurrentBranch = strings.TrimSpace(branch)
	}
	if originURL, err := gitOutput(ctx, repoRoot, "config", "--get", "remote.origin.url"); err == nil {
		resp.OriginUrl = strings.TrimSpace(originURL)
	}

	// The default branch name depends on git config; just verify it's non-empty.
	assert.NotEmpty(t, resp.CurrentBranch)
	assert.Equal(t, "https://github.com/test/repo.git", resp.OriginUrl)
}

func TestIsRemoteRef(t *testing.T) {
	dir := initRepo(t)
	ctx := context.Background()

	// Add a remote and create a remote tracking branch.
	remoteDir := initRepo(t)
	run(t, remoteDir, "git", "checkout", "-b", "feature/test")
	run(t, remoteDir, "git", "commit", "--allow-empty", "-m", "feature commit")
	run(t, dir, "git", "remote", "add", "origin", remoteDir)
	run(t, dir, "git", "fetch", "origin")

	// "origin/feature/test" should be a remote ref.
	assert.True(t, isRemoteRef(ctx, dir, "origin/feature/test"))

	// A local branch name should not be a remote ref.
	assert.False(t, isRemoteRef(ctx, dir, "main"))
	assert.False(t, isRemoteRef(ctx, dir, "nonexistent"))
}

func TestCheckoutBranchIfRequested_RemoteBranch(t *testing.T) {
	dir := initRepo(t)

	// Create a "remote" repo with a branch.
	remoteDir := initRepo(t)
	run(t, remoteDir, "git", "checkout", "-b", "feature/remote-test")
	run(t, remoteDir, "git", "commit", "--allow-empty", "-m", "remote commit")

	// Add as origin and fetch.
	run(t, dir, "git", "remote", "add", "origin", remoteDir)
	run(t, dir, "git", "fetch", "origin")

	// Checkout the remote branch via the service method.
	svc := &Context{}
	err := svc.checkoutBranchIfRequested(dir, "origin/feature/remote-test")
	require.NoError(t, err)

	// Verify we're on a local branch (not detached HEAD).
	ctx := context.Background()
	branch, err := gitOutput(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "feature/remote-test", strings.TrimSpace(branch))

	// Verify the local branch tracks the remote.
	upstream, err := gitOutput(ctx, dir, "rev-parse", "--abbrev-ref", "feature/remote-test@{upstream}")
	require.NoError(t, err)
	assert.Equal(t, "origin/feature/remote-test", strings.TrimSpace(upstream))
}

func TestCheckoutBranchIfRequested_RemoteBranchWithExistingLocal(t *testing.T) {
	dir := initRepo(t)

	// Create a "remote" repo with a branch.
	remoteDir := initRepo(t)
	run(t, remoteDir, "git", "checkout", "-b", "feature/existing")
	run(t, remoteDir, "git", "commit", "--allow-empty", "-m", "remote commit")

	// Add as origin and fetch.
	run(t, dir, "git", "remote", "add", "origin", remoteDir)
	run(t, dir, "git", "fetch", "origin")

	// Create a local branch with the same name.
	run(t, dir, "git", "checkout", "-b", "feature/existing")
	run(t, dir, "git", "commit", "--allow-empty", "-m", "local commit")
	run(t, dir, "git", "checkout", "-") // go back to default branch

	// Checkout the remote branch — should switch to existing local branch, not error.
	svc := &Context{}
	err := svc.checkoutBranchIfRequested(dir, "origin/feature/existing")
	require.NoError(t, err)

	ctx := context.Background()
	branch, err := gitOutput(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "feature/existing", strings.TrimSpace(branch))
}

func TestCheckoutBranchIfRequested_LocalBranch(t *testing.T) {
	dir := initRepo(t)

	// Create a local branch.
	run(t, dir, "git", "checkout", "-b", "my-feature")
	run(t, dir, "git", "commit", "--allow-empty", "-m", "feature commit")
	run(t, dir, "git", "checkout", "-") // go back to default branch

	// Checkout the local branch.
	svc := &Context{}
	err := svc.checkoutBranchIfRequested(dir, "my-feature")
	require.NoError(t, err)

	ctx := context.Background()
	branch, err := gitOutput(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "my-feature", strings.TrimSpace(branch))
}

func TestDetachedHEAD_ShowsShortSHA(t *testing.T) {
	dir := initRepo(t)
	ctx := context.Background()

	// Get short SHA before detaching.
	expectedSHA, err := gitOutput(ctx, dir, "rev-parse", "--short", "HEAD")
	require.NoError(t, err)
	expectedSHA = strings.TrimSpace(expectedSHA)

	// Detach HEAD.
	run(t, dir, "git", "checkout", "--detach", "HEAD")

	// Simulate what the handler does.
	branch, err := gitOutput(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	branch = strings.TrimSpace(branch)
	if branch == "HEAD" {
		sha, err := gitOutput(ctx, dir, "rev-parse", "--short", "HEAD")
		require.NoError(t, err)
		branch = strings.TrimSpace(sha)
	}

	assert.Equal(t, expectedSHA, branch)
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

func TestCurrentBranchForPath_LinkedWorktreeUsesWorktreeBranch(t *testing.T) {
	dir := initRepo(t)
	ctx := context.Background()

	run(t, dir, "git", "checkout", "-b", "main-branch")
	run(t, dir, "git", "commit", "--allow-empty", "-m", "main branch commit")

	wtDir := filepath.Join(t.TempDir(), "feature-worktree")
	run(t, dir, "git", "worktree", "add", "-b", "feature-branch", wtDir)

	require.Equal(t, "main-branch", currentBranchForPath(ctx, dir))
	require.Equal(t, "feature-branch", currentBranchForPath(ctx, wtDir))
}
