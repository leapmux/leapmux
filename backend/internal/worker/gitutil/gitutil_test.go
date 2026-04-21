package gitutil

import (
	"context"
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

		got := GetOriginURL(context.Background(), dir)
		assert.Equal(t, expected, got)
	})

	t.Run("returns SSH origin URL", func(t *testing.T) {
		dir := initRepo(t)
		expected := "git@github.com:example/repo.git"
		cmd := exec.Command("git", "-C", dir, "remote", "add", "origin", expected)
		require.NoError(t, cmd.Run())

		got := GetOriginURL(context.Background(), dir)
		assert.Equal(t, expected, got)
	})

	t.Run("returns empty string when no origin", func(t *testing.T) {
		dir := initRepo(t)
		got := GetOriginURL(context.Background(), dir)
		assert.Empty(t, got)
	})

	t.Run("returns empty string for non-git directory", func(t *testing.T) {
		dir := t.TempDir()
		got := GetOriginURL(context.Background(), dir)
		assert.Empty(t, got)
	})

	t.Run("returns empty string for nonexistent directory", func(t *testing.T) {
		got := GetOriginURL(context.Background(), filepath.Join(t.TempDir(), "nonexistent"))
		assert.Empty(t, got)
	})
}

func TestGetGitStatus_OriginURL(t *testing.T) {
	t.Run("includes origin URL in status", func(t *testing.T) {
		dir := initRepo(t)
		expected := "https://github.com/example/repo.git"
		cmd := exec.Command("git", "-C", dir, "remote", "add", "origin", expected)
		require.NoError(t, cmd.Run())

		status := GetGitStatus(context.Background(), dir)
		require.NotNil(t, status)
		assert.Equal(t, expected, status.OriginUrl)
	})

	t.Run("origin URL is empty when no remote", func(t *testing.T) {
		dir := initRepo(t)

		status := GetGitStatus(context.Background(), dir)
		require.NotNil(t, status)
		assert.Empty(t, status.OriginUrl)
	})
}

func TestGetGitStatus_Branch(t *testing.T) {
	dir := initRepo(t)
	status := GetGitStatus(context.Background(), dir)
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

	status := GetGitStatus(context.Background(), dir)
	require.NotNil(t, status)
	assert.True(t, status.Modified)
}

func TestGetGitStatus_Untracked(t *testing.T) {
	dir := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("new"), 0o644))

	status := GetGitStatus(context.Background(), dir)
	require.NotNil(t, status)
	assert.True(t, status.Untracked)
}

func TestGetGitStatus_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	status := GetGitStatus(context.Background(), dir)
	assert.Nil(t, status)
}

func TestGetGitStatus_DetachedHEAD(t *testing.T) {
	dir := initRepo(t)

	// Get the current commit SHA.
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD")
	out, err := cmd.Output()
	require.NoError(t, err)
	expectedSHA := string(out[:len(out)-1]) // trim newline

	// Detach HEAD by checking out the commit directly.
	cmd = exec.Command("git", "-C", dir, "checkout", "--detach", "HEAD")
	require.NoError(t, cmd.Run())

	status := GetGitStatus(context.Background(), dir)
	require.NotNil(t, status)
	// Should show short SHA, not empty string or "HEAD".
	assert.Equal(t, expectedSHA, status.Branch)
}

// TestBatchGetGitStatus_HappyPath runs BatchGetGitStatus across three
// distinct repositories and verifies every slot receives a non-nil
// status with the expected origin URL. This is the base case: no
// duplicates, no empty strings, N unique repos → N results.
func TestBatchGetGitStatus_HappyPath(t *testing.T) {
	dir1 := initRepo(t)
	dir2 := initRepo(t)
	dir3 := initRepo(t)
	for dir, origin := range map[string]string{
		dir1: "https://example.com/one.git",
		dir2: "https://example.com/two.git",
		dir3: "https://example.com/three.git",
	} {
		cmd := exec.Command("git", "-C", dir, "remote", "add", "origin", origin)
		require.NoError(t, cmd.Run())
	}

	results := BatchGetGitStatus(context.Background(), []string{dir1, dir2, dir3})
	require.Len(t, results, 3)
	assert.Equal(t, "https://example.com/one.git", results[0].GetOriginUrl())
	assert.Equal(t, "https://example.com/two.git", results[1].GetOriginUrl())
	assert.Equal(t, "https://example.com/three.git", results[2].GetOriginUrl())
}

// TestBatchGetGitStatus_DeduplicatesIdenticalPaths verifies that
// multiple slots pointing at the same directory receive the same
// status pointer (one git shell-out, reused across slots). This is
// the whole point of batching — multiple agent/terminal tabs on the
// same repo shouldn't pay N× `git status` cost.
func TestBatchGetGitStatus_DeduplicatesIdenticalPaths(t *testing.T) {
	dir := initRepo(t)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin", "https://example.com/shared.git")
	require.NoError(t, cmd.Run())

	results := BatchGetGitStatus(context.Background(), []string{dir, dir, dir, dir})
	require.Len(t, results, 4)
	// All slots must share the identical pointer — proof that the
	// shell-out ran once and the result was fan-out to every slot.
	for i := 1; i < len(results); i++ {
		assert.Same(t, results[0], results[i],
			"slot %d should share the dedup'd status pointer with slot 0", i)
	}
	assert.Equal(t, "https://example.com/shared.git", results[0].GetOriginUrl())
}

// TestBatchGetGitStatus_MixedDirsAndEmptyPaths covers the common case:
// some slots are real repos, some share a repo, some are empty string
// (e.g. terminals with no shell_start_dir). Empty-string slots must
// map to nil; duplicates must dedupe; unique repos must get their own
// status.
func TestBatchGetGitStatus_MixedDirsAndEmptyPaths(t *testing.T) {
	repoA := initRepo(t)
	repoB := initRepo(t)
	require.NoError(t, exec.Command("git", "-C", repoA, "remote", "add", "origin", "https://example.com/a.git").Run())
	require.NoError(t, exec.Command("git", "-C", repoB, "remote", "add", "origin", "https://example.com/b.git").Run())

	results := BatchGetGitStatus(context.Background(), []string{repoA, "", repoA, repoB, ""})
	require.Len(t, results, 5)

	assert.NotNil(t, results[0])
	assert.Nil(t, results[1], "empty path must map to nil")
	assert.Same(t, results[0], results[2], "repoA slots must share the dedup'd pointer")
	assert.NotNil(t, results[3])
	assert.NotSame(t, results[0], results[3], "different repos must yield different results")
	assert.Nil(t, results[4], "second empty path must also map to nil")

	assert.Equal(t, "https://example.com/a.git", results[0].GetOriginUrl())
	assert.Equal(t, "https://example.com/b.git", results[3].GetOriginUrl())
}

// TestBatchGetGitStatus_NonGitDirsYieldNil verifies that
// non-repository paths flow through as nil (matching GetGitStatus's
// behavior for non-repo directories). Mixed with a real repo so we
// also confirm the real one succeeds alongside.
func TestBatchGetGitStatus_NonGitDirsYieldNil(t *testing.T) {
	repo := initRepo(t)
	notGit := t.TempDir()

	results := BatchGetGitStatus(context.Background(), []string{repo, notGit})
	require.Len(t, results, 2)
	assert.NotNil(t, results[0])
	assert.Nil(t, results[1], "non-git directory should map to nil")
}

// TestBatchGetGitStatus_EmptyInput handles the empty-slice edge case
// that would otherwise panic or hang if the fan-out logic assumes ≥1
// entry.
func TestBatchGetGitStatus_EmptyInput(t *testing.T) {
	results := BatchGetGitStatus(context.Background(), nil)
	assert.Empty(t, results)

	results = BatchGetGitStatus(context.Background(), []string{})
	assert.Empty(t, results)
}

// TestBatchGetGitStatus_AllEmptyPathsYieldNilSlice confirms that a
// batch of only empty-string inputs produces a slice of the correct
// length, fully populated with nil — no git shell-outs run.
func TestBatchGetGitStatus_AllEmptyPathsYieldNilSlice(t *testing.T) {
	results := BatchGetGitStatus(context.Background(), []string{"", "", ""})
	require.Len(t, results, 3)
	for i, r := range results {
		assert.Nil(t, r, "slot %d should be nil for empty-path input", i)
	}
}

// TestGetGitStatus_HonorsCtxCancellation pins the cancellation contract
// runAgentStartup depends on: when CloseAgent lands during the phase-1
// "Checking Git status…" shell-out, the startup context cancels and the
// in-flight `git` processes must not continue — GetGitStatus returns nil
// (the failure path) and does not block past the cancel.
func TestGetGitStatus_HonorsCtxCancellation(t *testing.T) {
	dir := initRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before call

	status := GetGitStatus(ctx, dir)
	assert.Nil(t, status,
		"GetGitStatus must surface cancellation as a nil result, not silently run git to completion")
}
