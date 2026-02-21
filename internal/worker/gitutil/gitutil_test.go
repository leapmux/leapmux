package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolvedTempDir returns a temp directory with symlinks resolved (e.g. /var -> /private/var on macOS).
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return resolved
}

// initGitRepo creates a git repo in dir with an initial commit.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "git", "init")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	run(t, dir, "git", "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello"), 0o644))
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "initial")
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "command %q failed: %s", append([]string{name}, args...), string(output))
}

func TestGetGitInfo_RegularRepo(t *testing.T) {
	dir := resolvedTempDir(t)
	initGitRepo(t, dir)

	info, err := GetGitInfo(dir)
	require.NoError(t, err)
	assert.True(t, info.IsGitRepo)
	assert.False(t, info.IsWorktree)
	assert.Equal(t, dir, info.RepoRoot)
	assert.Equal(t, filepath.Base(dir), info.RepoDirName)
	assert.True(t, info.IsRepoRoot)
}

func TestGetGitInfo_Worktree(t *testing.T) {
	dir := resolvedTempDir(t)
	repoDir := filepath.Join(dir, "myrepo")
	require.NoError(t, os.Mkdir(repoDir, 0o755))
	initGitRepo(t, repoDir)

	wtDir := filepath.Join(dir, "myrepo-worktrees", "feature")
	run(t, repoDir, "git", "worktree", "add", wtDir, "-b", "feature")

	info, err := GetGitInfo(wtDir)
	require.NoError(t, err)
	assert.True(t, info.IsGitRepo)
	assert.True(t, info.IsWorktree)
	assert.Equal(t, repoDir, info.RepoRoot)
	assert.Equal(t, "myrepo", info.RepoDirName)
	assert.False(t, info.IsRepoRoot, "worktree directory should not be the repo root")
}

func TestGetGitInfo_NotGitRepo(t *testing.T) {
	dir := resolvedTempDir(t)

	info, err := GetGitInfo(dir)
	require.NoError(t, err)
	assert.False(t, info.IsGitRepo)
	assert.False(t, info.IsRepoRoot)
}

func TestGetGitInfo_NestedSubdir(t *testing.T) {
	dir := resolvedTempDir(t)
	initGitRepo(t, dir)

	subdir := filepath.Join(dir, "a", "b", "c")
	require.NoError(t, os.MkdirAll(subdir, 0o755))

	info, err := GetGitInfo(subdir)
	require.NoError(t, err)
	assert.True(t, info.IsGitRepo)
	assert.False(t, info.IsWorktree)
	assert.Equal(t, dir, info.RepoRoot)
	assert.False(t, info.IsRepoRoot, "nested subdir should not be the repo root")
}

func TestCreateWorktree_NewBranch(t *testing.T) {
	dir := resolvedTempDir(t)
	repoDir := filepath.Join(dir, "repo")
	require.NoError(t, os.Mkdir(repoDir, 0o755))
	initGitRepo(t, repoDir)

	wtDir := filepath.Join(dir, "repo-worktrees", "new-feature")
	err := CreateWorktree(repoDir, wtDir, "new-feature")
	require.NoError(t, err)

	// Verify the worktree directory exists.
	_, err = os.Stat(wtDir)
	require.NoError(t, err)

	// Verify we're on the right branch.
	cmd := exec.Command("git", "-C", wtDir, "branch", "--show-current")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "new-feature", trimOutput(output))
}

func TestCreateWorktree_ExistingBranch(t *testing.T) {
	dir := resolvedTempDir(t)
	repoDir := filepath.Join(dir, "repo")
	require.NoError(t, os.Mkdir(repoDir, 0o755))
	initGitRepo(t, repoDir)

	// Create a branch first.
	run(t, repoDir, "git", "branch", "existing-branch")

	wtDir := filepath.Join(dir, "repo-worktrees", "existing-branch")
	err := CreateWorktree(repoDir, wtDir, "existing-branch")
	require.NoError(t, err)

	// Verify the worktree is on the existing branch.
	cmd := exec.Command("git", "-C", wtDir, "branch", "--show-current")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "existing-branch", trimOutput(output))
}

func TestCreateWorktree_InvalidBranch(t *testing.T) {
	dir := resolvedTempDir(t)
	repoDir := filepath.Join(dir, "repo")
	require.NoError(t, os.Mkdir(repoDir, 0o755))
	initGitRepo(t, repoDir)

	wtDir := filepath.Join(dir, "repo-worktrees", "bad")
	err := CreateWorktree(repoDir, wtDir, "bad..branch")
	assert.Error(t, err)
}

func TestCreateWorktree_PathExists(t *testing.T) {
	dir := resolvedTempDir(t)
	repoDir := filepath.Join(dir, "repo")
	require.NoError(t, os.Mkdir(repoDir, 0o755))
	initGitRepo(t, repoDir)

	// Pre-create the target path.
	wtDir := filepath.Join(dir, "repo-worktrees", "taken")
	require.NoError(t, os.MkdirAll(wtDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "file.txt"), []byte("block"), 0o644))

	err := CreateWorktree(repoDir, wtDir, "taken")
	assert.Error(t, err)
}

func TestIsWorktreeClean_Clean(t *testing.T) {
	dir := resolvedTempDir(t)
	repoDir := filepath.Join(dir, "repo")
	require.NoError(t, os.Mkdir(repoDir, 0o755))
	initGitRepo(t, repoDir)

	wtDir := filepath.Join(dir, "repo-worktrees", "clean")
	run(t, repoDir, "git", "worktree", "add", wtDir, "-b", "clean")

	clean, err := IsWorktreeClean(wtDir)
	require.NoError(t, err)
	assert.True(t, clean)
}

func TestIsWorktreeClean_UncommittedChanges(t *testing.T) {
	dir := resolvedTempDir(t)
	repoDir := filepath.Join(dir, "repo")
	require.NoError(t, os.Mkdir(repoDir, 0o755))
	initGitRepo(t, repoDir)

	wtDir := filepath.Join(dir, "repo-worktrees", "dirty")
	run(t, repoDir, "git", "worktree", "add", wtDir, "-b", "dirty")

	// Make uncommitted changes.
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "new-file.txt"), []byte("dirty"), 0o644))

	clean, err := IsWorktreeClean(wtDir)
	require.NoError(t, err)
	assert.False(t, clean)
}

func TestIsWorktreeClean_UnpushedCommits(t *testing.T) {
	dir := resolvedTempDir(t)

	// Create a bare "remote" repo.
	remoteDir := filepath.Join(dir, "remote.git")
	run(t, dir, "git", "init", "--bare", remoteDir)

	// Create main repo and push to remote.
	repoDir := filepath.Join(dir, "repo")
	require.NoError(t, os.Mkdir(repoDir, 0o755))
	initGitRepo(t, repoDir)
	run(t, repoDir, "git", "remote", "add", "origin", remoteDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")

	// Create worktree with tracking.
	wtDir := filepath.Join(dir, "repo-worktrees", "unpushed")
	run(t, repoDir, "git", "worktree", "add", wtDir, "-b", "unpushed")
	run(t, wtDir, "git", "push", "-u", "origin", "unpushed")

	// Make a local commit without pushing.
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "local.txt"), []byte("local"), 0o644))
	run(t, wtDir, "git", "add", ".")
	run(t, wtDir, "git", "commit", "-m", "local commit")

	clean, err := IsWorktreeClean(wtDir)
	require.NoError(t, err)
	assert.False(t, clean)
}

func TestIsWorktreeClean_BothDirty(t *testing.T) {
	dir := resolvedTempDir(t)

	remoteDir := filepath.Join(dir, "remote.git")
	run(t, dir, "git", "init", "--bare", remoteDir)

	repoDir := filepath.Join(dir, "repo")
	require.NoError(t, os.Mkdir(repoDir, 0o755))
	initGitRepo(t, repoDir)
	run(t, repoDir, "git", "remote", "add", "origin", remoteDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")

	wtDir := filepath.Join(dir, "repo-worktrees", "both")
	run(t, repoDir, "git", "worktree", "add", wtDir, "-b", "both")
	run(t, wtDir, "git", "push", "-u", "origin", "both")

	// Unpushed commit.
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "committed.txt"), []byte("c"), 0o644))
	run(t, wtDir, "git", "add", ".")
	run(t, wtDir, "git", "commit", "-m", "local")

	// Uncommitted changes.
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "uncommitted.txt"), []byte("u"), 0o644))

	clean, err := IsWorktreeClean(wtDir)
	require.NoError(t, err)
	assert.False(t, clean)
}

func TestIsWorktreeClean_NoUpstreamWithLocalCommits(t *testing.T) {
	dir := resolvedTempDir(t)
	repoDir := filepath.Join(dir, "repo")
	require.NoError(t, os.Mkdir(repoDir, 0o755))
	initGitRepo(t, repoDir)

	// Create a worktree (no remote configured, so no upstream).
	wtDir := filepath.Join(dir, "repo-worktrees", "no-upstream")
	run(t, repoDir, "git", "worktree", "add", wtDir, "-b", "no-upstream")

	// Make a local commit — this commit only exists on this branch.
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "local.txt"), []byte("only here"), 0o644))
	run(t, wtDir, "git", "add", ".")
	run(t, wtDir, "git", "commit", "-m", "local only commit")

	clean, err := IsWorktreeClean(wtDir)
	require.NoError(t, err)
	assert.False(t, clean, "worktree with local-only commits (no upstream) should be dirty")
}

func TestIsWorktreeClean_NoUpstreamNoDivergence(t *testing.T) {
	dir := resolvedTempDir(t)
	repoDir := filepath.Join(dir, "repo")
	require.NoError(t, os.Mkdir(repoDir, 0o755))
	initGitRepo(t, repoDir)

	// Create a worktree (no remote, no upstream) but don't add any new commits.
	// The worktree branch starts at the same commit as the main branch.
	wtDir := filepath.Join(dir, "repo-worktrees", "fresh")
	run(t, repoDir, "git", "worktree", "add", wtDir, "-b", "fresh")

	clean, err := IsWorktreeClean(wtDir)
	require.NoError(t, err)
	assert.True(t, clean, "freshly created worktree with no new commits should be clean")
}

func TestRemoveWorktree_Clean(t *testing.T) {
	dir := resolvedTempDir(t)
	repoDir := filepath.Join(dir, "repo")
	require.NoError(t, os.Mkdir(repoDir, 0o755))
	initGitRepo(t, repoDir)

	wtDir := filepath.Join(dir, "repo-worktrees", "removeme")
	run(t, repoDir, "git", "worktree", "add", wtDir, "-b", "removeme")

	err := RemoveWorktree(repoDir, wtDir)
	require.NoError(t, err)

	// Verify directory is gone.
	_, err = os.Stat(wtDir)
	assert.True(t, os.IsNotExist(err))

	// Verify empty parent directory was also cleaned up.
	_, err = os.Stat(filepath.Dir(wtDir))
	assert.True(t, os.IsNotExist(err))
}

func TestRemoveWorktree_ParentKeptWhenNotEmpty(t *testing.T) {
	dir := resolvedTempDir(t)
	repoDir := filepath.Join(dir, "repo")
	require.NoError(t, os.Mkdir(repoDir, 0o755))
	initGitRepo(t, repoDir)

	parentDir := filepath.Join(dir, "repo-worktrees")
	wt1 := filepath.Join(parentDir, "branch1")
	wt2 := filepath.Join(parentDir, "branch2")
	run(t, repoDir, "git", "worktree", "add", wt1, "-b", "branch1")
	run(t, repoDir, "git", "worktree", "add", wt2, "-b", "branch2")

	// Remove only one worktree.
	err := RemoveWorktree(repoDir, wt1)
	require.NoError(t, err)

	// Verify the removed worktree is gone.
	_, err = os.Stat(wt1)
	assert.True(t, os.IsNotExist(err))

	// Verify the parent directory still exists (wt2 is still there).
	_, err = os.Stat(parentDir)
	assert.NoError(t, err)

	// Remove the second worktree.
	err = RemoveWorktree(repoDir, wt2)
	require.NoError(t, err)

	// Now the parent should be cleaned up.
	_, err = os.Stat(parentDir)
	assert.True(t, os.IsNotExist(err))
}

func TestRemoveWorktree_NonExistent(t *testing.T) {
	dir := resolvedTempDir(t)
	repoDir := filepath.Join(dir, "repo")
	require.NoError(t, os.Mkdir(repoDir, 0o755))
	initGitRepo(t, repoDir)

	wtDir := filepath.Join(dir, "repo-worktrees", "nonexistent")
	err := RemoveWorktree(repoDir, wtDir)
	// Should not error since the directory doesn't exist (nothing to remove).
	// git worktree remove on non-existent will error, but our fallback handles it.
	assert.NoError(t, err)
}

func trimOutput(b []byte) string {
	return string(b[:max(0, len(b)-1)]) // strip trailing newline
}

// --- Tests for git status parsing ---

func TestParseStatusV2_BranchAndTracking(t *testing.T) {
	input := []byte("# branch.oid abc123\n# branch.head main\n# branch.upstream origin/main\n# branch.ab +3 -1\n")
	status := &GitStatus{}
	parseStatusV2(input, status)

	assert.Equal(t, "main", status.Branch)
	assert.Equal(t, 3, status.Ahead)
	assert.Equal(t, 1, status.Behind)
}

func TestParseStatusV2_DetachedHead(t *testing.T) {
	input := []byte("# branch.oid abc123\n# branch.head (detached)\n")
	status := &GitStatus{}
	parseStatusV2(input, status)

	assert.Empty(t, status.Branch, "Branch should be empty for detached HEAD")
}

func TestParseStatusV2_OrdinaryModified(t *testing.T) {
	input := []byte("1 M. N... 100644 100644 100644 abc123 def456 file.go\n")
	status := &GitStatus{}
	parseStatusV2(input, status)

	assert.True(t, status.Modified, "Modified should be true for M in staging")
}

func TestParseStatusV2_AddedAndDeleted(t *testing.T) {
	input := []byte("1 A. N... 100644 100644 100644 abc123 def456 new.go\n1 .D N... 100644 100644 100644 abc123 def456 old.go\n")
	status := &GitStatus{}
	parseStatusV2(input, status)

	assert.True(t, status.Added)
	assert.True(t, status.Deleted)
}

func TestParseStatusV2_Renamed(t *testing.T) {
	input := []byte("2 R. N... 100644 100644 100644 abc123 def456 R100 new.go\told.go\n")
	status := &GitStatus{}
	parseStatusV2(input, status)

	assert.True(t, status.Renamed)
}

func TestParseStatusV2_Unmerged(t *testing.T) {
	input := []byte("u UU N... 100644 100644 100644 100644 abc123 def456 ghi789 conflict.go\n")
	status := &GitStatus{}
	parseStatusV2(input, status)

	assert.True(t, status.Conflicted)
}

func TestParseStatusV2_Untracked(t *testing.T) {
	input := []byte("? newfile.txt\n")
	status := &GitStatus{}
	parseStatusV2(input, status)

	assert.True(t, status.Untracked)
}

func TestParseStatusV2_TypeChanged(t *testing.T) {
	input := []byte("1 T. N... 120000 100644 100644 abc123 def456 link.go\n")
	status := &GitStatus{}
	parseStatusV2(input, status)

	assert.True(t, status.TypeChanged)
}

func TestParseStatusV2_MixedStatus(t *testing.T) {
	input := []byte(
		"# branch.head feature/test\n" +
			"# branch.ab +1 -0\n" +
			"1 M. N... 100644 100644 100644 abc123 def456 modified.go\n" +
			"1 A. N... 100644 100644 100644 abc123 def456 added.go\n" +
			"2 R. N... 100644 100644 100644 abc123 def456 R100 new.go\told.go\n" +
			"? untracked.txt\n",
	)
	status := &GitStatus{}
	parseStatusV2(input, status)

	assert.Equal(t, "feature/test", status.Branch)
	assert.Equal(t, 1, status.Ahead)
	assert.True(t, status.Modified)
	assert.True(t, status.Added)
	assert.True(t, status.Renamed)
	assert.True(t, status.Untracked)
	assert.False(t, status.Deleted)
	assert.False(t, status.Conflicted)
}

func TestParseStatusV2_EmptyOutput(t *testing.T) {
	status := &GitStatus{}
	parseStatusV2([]byte(""), status)

	assert.Empty(t, status.Branch)
	assert.False(t, status.Modified)
	assert.False(t, status.Added)
	assert.False(t, status.Deleted)
	assert.False(t, status.Renamed)
	assert.False(t, status.Untracked)
	assert.False(t, status.Conflicted)
	assert.False(t, status.TypeChanged)
}

func TestParseStatusV2_CleanRepo(t *testing.T) {
	input := []byte("# branch.oid abc123\n# branch.head main\n# branch.upstream origin/main\n# branch.ab +0 -0\n")
	status := &GitStatus{}
	parseStatusV2(input, status)

	assert.Equal(t, "main", status.Branch)
	assert.Equal(t, 0, status.Ahead)
	assert.Equal(t, 0, status.Behind)
	assert.False(t, status.Modified)
	assert.False(t, status.Added)
	assert.False(t, status.Deleted)
	assert.False(t, status.Renamed)
	assert.False(t, status.Untracked)
	assert.False(t, status.Conflicted)
	assert.False(t, status.TypeChanged)
	assert.False(t, status.Stashed)
}

func TestParseXY(t *testing.T) {
	tests := []struct {
		name        string
		x, y        byte
		modified    bool
		added       bool
		deleted     bool
		typeChanged bool
		renamed     bool
	}{
		{"staged modified", 'M', '.', true, false, false, false, false},
		{"worktree modified", '.', 'M', true, false, false, false, false},
		{"staged added", 'A', '.', false, true, false, false, false},
		{"staged deleted", 'D', '.', false, false, true, false, false},
		{"worktree deleted", '.', 'D', false, false, true, false, false},
		{"type changed", 'T', '.', false, false, false, true, false},
		{"renamed", 'R', '.', false, false, false, false, true},
		{"both modified", 'M', 'M', true, false, false, false, false},
		{"no change", '.', '.', false, false, false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := &GitStatus{}
			parseXY(tt.x, tt.y, status)
			assert.Equal(t, tt.modified, status.Modified, "Modified")
			assert.Equal(t, tt.added, status.Added, "Added")
			assert.Equal(t, tt.deleted, status.Deleted, "Deleted")
			assert.Equal(t, tt.typeChanged, status.TypeChanged, "TypeChanged")
			assert.Equal(t, tt.renamed, status.Renamed, "Renamed")
		})
	}
}

func TestGetGitStatus_Integration(t *testing.T) {
	dir := resolvedTempDir(t)
	initGitRepo(t, dir)

	// Clean repo should have branch but no flags.
	status := GetGitStatus(dir)
	require.NotNil(t, status)
	assert.NotEmpty(t, status.Branch) // "main" or "master" depending on git config
	assert.False(t, status.Modified)
	assert.False(t, status.Untracked)

	// Add an untracked file.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("hello"), 0o644))
	status = GetGitStatus(dir)
	require.NotNil(t, status)
	assert.True(t, status.Untracked)

	// Stage and modify.
	run(t, dir, "git", "add", "untracked.txt")
	status = GetGitStatus(dir)
	require.NotNil(t, status)
	assert.True(t, status.Added)
	assert.False(t, status.Untracked)
}

func TestGetGitStatus_NotGitRepo(t *testing.T) {
	dir := resolvedTempDir(t)
	status := GetGitStatus(dir)
	assert.Nil(t, status, "should return nil for non-git directory")
}

func TestGetGitStatus_Stash(t *testing.T) {
	dir := resolvedTempDir(t)
	initGitRepo(t, dir)

	// Create a stash.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stashme.txt"), []byte("stash"), 0o644))
	run(t, dir, "git", "add", "stashme.txt")
	run(t, dir, "git", "stash")

	status := GetGitStatus(dir)
	require.NotNil(t, status)
	assert.True(t, status.Stashed)
}
