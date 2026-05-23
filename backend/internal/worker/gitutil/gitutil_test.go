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

func TestGetToplevel(t *testing.T) {
	t.Run("returns absolute repo root for repo root", func(t *testing.T) {
		dir := initRepo(t)
		got := GetToplevel(context.Background(), dir)
		require.NotEmpty(t, got)
		expected, err := filepath.EvalSymlinks(dir)
		require.NoError(t, err)
		gotResolved, err := filepath.EvalSymlinks(got)
		require.NoError(t, err)
		assert.Equal(t, expected, gotResolved)
	})

	t.Run("returns repo root when called from a subdirectory", func(t *testing.T) {
		dir := initRepo(t)
		sub := filepath.Join(dir, "src", "nested")
		require.NoError(t, os.MkdirAll(sub, 0o755))

		got := GetToplevel(context.Background(), sub)
		require.NotEmpty(t, got)
		expected, err := filepath.EvalSymlinks(dir)
		require.NoError(t, err)
		gotResolved, err := filepath.EvalSymlinks(got)
		require.NoError(t, err)
		assert.Equal(t, expected, gotResolved)
	})

	t.Run("returns empty string for non-git directory", func(t *testing.T) {
		dir := t.TempDir()
		got := GetToplevel(context.Background(), dir)
		assert.Empty(t, got)
	})

	t.Run("returns empty string for nonexistent directory", func(t *testing.T) {
		got := GetToplevel(context.Background(), filepath.Join(t.TempDir(), "nope"))
		assert.Empty(t, got)
	})
}

func TestGetToplevelInfo(t *testing.T) {
	// GetToplevelInfo bundles the toplevel probe with the worktree
	// disposition so one rev-parse answers both. The disposition is
	// derived by comparing `--git-dir` to `--git-common-dir` — they
	// differ only for linked worktrees.
	t.Run("non-worktree repo: IsWorktree=false, Toplevel matches repo root", func(t *testing.T) {
		dir := initRepo(t)
		info := GetToplevelInfo(context.Background(), dir)
		assert.False(t, info.IsWorktree, "main repo root must not be flagged as a worktree")
		require.NotEmpty(t, info.Toplevel)
		expected, err := filepath.EvalSymlinks(dir)
		require.NoError(t, err)
		gotResolved, err := filepath.EvalSymlinks(info.Toplevel)
		require.NoError(t, err)
		assert.Equal(t, expected, gotResolved)
	})

	t.Run("linked worktree: IsWorktree=true, Toplevel is the worktree dir", func(t *testing.T) {
		repoDir := initRepo(t)
		wtDir := filepath.Join(t.TempDir(), "wt")
		cmd := exec.Command("git", "-C", repoDir, "worktree", "add", "-b", "wt-branch", wtDir)
		require.NoError(t, cmd.Run())

		info := GetToplevelInfo(context.Background(), wtDir)
		assert.True(t, info.IsWorktree, "linked worktree must be flagged")
		require.NotEmpty(t, info.Toplevel)
		expected, err := filepath.EvalSymlinks(wtDir)
		require.NoError(t, err)
		gotResolved, err := filepath.EvalSymlinks(info.Toplevel)
		require.NoError(t, err)
		assert.Equal(t, expected, gotResolved, "Toplevel must point at the worktree dir, not the main repo root")
	})

	t.Run("non-git directory: zero value", func(t *testing.T) {
		info := GetToplevelInfo(context.Background(), t.TempDir())
		assert.Empty(t, info.Toplevel)
		assert.False(t, info.IsWorktree)
	})

	t.Run("subdirectory of a linked worktree still reports IsWorktree=true", func(t *testing.T) {
		// `--git-dir` vs `--git-common-dir` is invariant to the subdir
		// the probe runs from, so deep paths inside a worktree must
		// still flip the worktree flag.
		repoDir := initRepo(t)
		wtDir := filepath.Join(t.TempDir(), "wt")
		cmd := exec.Command("git", "-C", repoDir, "worktree", "add", "-b", "deep-wt", wtDir)
		require.NoError(t, cmd.Run())
		sub := filepath.Join(wtDir, "a", "b")
		require.NoError(t, os.MkdirAll(sub, 0o755))

		info := GetToplevelInfo(context.Background(), sub)
		assert.True(t, info.IsWorktree)
	})

	t.Run("repo path containing a newline still parses (last two lines are the .git dirs)", func(t *testing.T) {
		// Regression: the parser used to require exactly 3 newline-
		// separated fields and returned the zero value for anything
		// else, silently dropping a repo whose path contained a newline
		// (legal POSIX, very rare). Counting from the end — the .git
		// paths are the last two lines, everything before them is the
		// (possibly multi-line) toplevel — handles the edge case.
		//
		// Skip on systems where the FS rejects newlines in directory
		// names (Windows + some macOS sandboxes).
		base := t.TempDir()
		dirty := filepath.Join(base, "with\nnewline")
		if err := os.MkdirAll(dirty, 0o755); err != nil {
			t.Skipf("filesystem rejected newline-in-path: %v", err)
		}
		cmd := exec.Command("git", "-C", dirty, "init")
		if err := cmd.Run(); err != nil {
			t.Skipf("git refused newline-in-path: %v", err)
		}
		info := GetToplevelInfo(context.Background(), dirty)
		// IsWorktree must still be reportable (no zero-value short-
		// circuit), and Toplevel must NOT collapse to empty.
		assert.False(t, info.IsWorktree, "main repo with newline in path must not be flagged as worktree")
		assert.NotEmpty(t, info.Toplevel, "newline-in-path must not silently zero Toplevel")
	})
}

func TestGetGitStatus_IsWorktree(t *testing.T) {
	// fanoutGitStatusProbes routes through GetToplevelInfo so every
	// AgentGitStatus / Terminal git probe carries the worktree
	// disposition. Pin both ends of the dichotomy so a future probe
	// reshuffle can't drop the field.
	t.Run("populates IsWorktree=false for a main repo", func(t *testing.T) {
		dir := initRepo(t)
		status := GetGitStatus(context.Background(), dir)
		require.NotNil(t, status)
		assert.False(t, status.GetIsWorktree())
		assert.NotEmpty(t, status.GetToplevel())
	})

	t.Run("populates IsWorktree=true for a linked worktree", func(t *testing.T) {
		repoDir := initRepo(t)
		wtDir := filepath.Join(t.TempDir(), "wt")
		cmd := exec.Command("git", "-C", repoDir, "worktree", "add", "-b", "status-wt", wtDir)
		require.NoError(t, cmd.Run())
		status := GetGitStatus(context.Background(), wtDir)
		require.NotNil(t, status)
		assert.True(t, status.GetIsWorktree(), "AgentGitStatus.IsWorktree must mirror the worktree disposition so DeleteBranchDialog can hint without re-probing")
	})
}

func TestGetGitStatus_Toplevel(t *testing.T) {
	t.Run("populates toplevel for a regular repo", func(t *testing.T) {
		dir := initRepo(t)
		status := GetGitStatus(context.Background(), dir)
		require.NotNil(t, status)
		require.NotEmpty(t, status.Toplevel)
		expected, err := filepath.EvalSymlinks(dir)
		require.NoError(t, err)
		got, err := filepath.EvalSymlinks(status.Toplevel)
		require.NoError(t, err)
		assert.Equal(t, expected, got)
	})

	t.Run("distinct repos yield distinct toplevels", func(t *testing.T) {
		repoA := initRepo(t)
		repoB := initRepo(t)
		sa := GetGitStatus(context.Background(), repoA)
		sb := GetGitStatus(context.Background(), repoB)
		require.NotNil(t, sa)
		require.NotNil(t, sb)
		assert.NotEmpty(t, sa.Toplevel)
		assert.NotEmpty(t, sb.Toplevel)
		assert.NotEqual(t, sa.Toplevel, sb.Toplevel)
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

// TestIsBranchInUse_HonorsCtxCancellation pins the cancellation contract
// the validateCreateWorktree errgroup depends on: when the parent ctx
// cancels mid-call (e.g. the dialog tears down before the worktree-list
// shell-out returns), IsBranchInUse must surface the cancellation as an
// error instead of letting the git subprocess run to completion.
func TestIsBranchInUse_HonorsCtxCancellation(t *testing.T) {
	dir := initRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before call

	inUse, err := IsBranchInUse(ctx, dir, "main")
	require.Error(t, err,
		"IsBranchInUse must surface cancellation as an error, not silently run git to completion")
	assert.False(t, inUse, "cancelled call must not report a false-positive branch usage")
}

func TestLookupRef(t *testing.T) {
	t.Run("local branch only", func(t *testing.T) {
		dir := initRepo(t)
		// Create a local branch on the existing commit.
		require.NoError(t, exec.Command("git", "-C", dir, "branch", "feature").Run())

		local, remote, err := LookupRef(context.Background(), dir, "feature")
		require.NoError(t, err)
		assert.True(t, local)
		assert.False(t, remote)
	})

	t.Run("remote-tracking ref only", func(t *testing.T) {
		dir := initRepo(t)
		// Stage a remote-tracking ref under refs/remotes/origin/feature
		// without a matching local branch.
		require.NoError(t, exec.Command("git", "-C", dir, "update-ref", "refs/remotes/origin/feature", "HEAD").Run())

		local, remote, err := LookupRef(context.Background(), dir, "origin/feature")
		require.NoError(t, err)
		assert.False(t, local)
		assert.True(t, remote)
	})

	t.Run("both local and remote", func(t *testing.T) {
		dir := initRepo(t)
		require.NoError(t, exec.Command("git", "-C", dir, "branch", "feature").Run())
		require.NoError(t, exec.Command("git", "-C", dir, "update-ref", "refs/remotes/origin/feature", "HEAD").Run())

		// Local lookup for "feature" hits refs/heads/feature; a hypothetical
		// remote-tracking ref refs/remotes/feature doesn't exist (the remote
		// one would be `origin/feature`), so only local should be true.
		local, remote, err := LookupRef(context.Background(), dir, "feature")
		require.NoError(t, err)
		assert.True(t, local)
		assert.False(t, remote)
	})

	t.Run("missing branch returns both false without error", func(t *testing.T) {
		dir := initRepo(t)
		local, remote, err := LookupRef(context.Background(), dir, "nonexistent")
		require.NoError(t, err)
		assert.False(t, local)
		assert.False(t, remote)
	})

	t.Run("non-git directory surfaces git error", func(t *testing.T) {
		dir := t.TempDir()
		_, _, err := LookupRef(context.Background(), dir, "main")
		// Outside a repo, git exits with a code other than 1 (typically 128),
		// so the helper does NOT swallow it — callers can distinguish "no
		// refs found" from "git crashed".
		require.Error(t, err)
	})

	t.Run("branch name with slash resolves intact", func(t *testing.T) {
		// Refs like "feature/foo" are legal — make sure the helper
		// doesn't treat the slash as a remote separator or otherwise
		// mangle the ref path it asks `git show-ref` for.
		dir := initRepo(t)
		require.NoError(t, exec.Command("git", "-C", dir, "branch", "feature/foo").Run())

		local, remote, err := LookupRef(context.Background(), dir, "feature/foo")
		require.NoError(t, err)
		assert.True(t, local)
		assert.False(t, remote)
	})

	t.Run("similarly-named refs do not bleed into each other", func(t *testing.T) {
		// Regression guard: if the helper ever compares ref names by
		// substring rather than equality, a `refs/heads/feature` lookup
		// might falsely match a `refs/heads/feature-branch` ref. Test
		// that overlapping prefixes don't trigger a false positive.
		dir := initRepo(t)
		require.NoError(t, exec.Command("git", "-C", dir, "branch", "feature-branch").Run())

		local, remote, err := LookupRef(context.Background(), dir, "feature")
		require.NoError(t, err)
		assert.False(t, local, "feature-branch must not satisfy a feature lookup")
		assert.False(t, remote)
	})

	t.Run("honours context cancellation", func(t *testing.T) {
		// The worktree validators run LookupRef inside an errgroup with
		// the dialog's context — make sure a cancelled context kills
		// the subprocess instead of letting it hang.
		dir := initRepo(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		local, remote, err := LookupRef(ctx, dir, "main")
		require.Error(t, err)
		assert.False(t, local)
		assert.False(t, remote)
	})
}

func TestHasRefs(t *testing.T) {
	t.Run("returns map keyed by input ref strings", func(t *testing.T) {
		dir := initRepo(t)
		require.NoError(t, exec.Command("git", "-C", dir, "branch", "feature").Run())
		require.NoError(t, exec.Command("git", "-C", dir, "update-ref", "refs/remotes/origin/main", "HEAD").Run())

		found, err := HasRefs(context.Background(), dir,
			"refs/heads/feature",
			"refs/remotes/origin/main",
			"refs/heads/missing",
		)
		require.NoError(t, err)
		assert.True(t, found["refs/heads/feature"])
		assert.True(t, found["refs/remotes/origin/main"])
		assert.False(t, found["refs/heads/missing"])
	})

	t.Run("empty refs returns empty map without forking git", func(t *testing.T) {
		// The "no git binary" fallback is implicit — a non-existent dir
		// must not produce an error when the refs slice is empty, since
		// we should not be invoking git at all.
		found, err := HasRefs(context.Background(), filepath.Join(t.TempDir(), "no-repo-here"))
		require.NoError(t, err)
		assert.NotNil(t, found, "empty result must be a non-nil map, not nil")
		assert.Empty(t, found)
	})

	t.Run("all-missing refs return non-nil empty map (show-ref exit 1)", func(t *testing.T) {
		dir := initRepo(t)
		found, err := HasRefs(context.Background(), dir, "refs/heads/none", "refs/remotes/none/none")
		require.NoError(t, err, "show-ref exit code 1 (no refs found) is a success probe, not an error")
		assert.NotNil(t, found)
		assert.False(t, found["refs/heads/none"])
		assert.False(t, found["refs/remotes/none/none"])
	})

	t.Run("non-git directory surfaces a real error", func(t *testing.T) {
		dir := t.TempDir()
		_, err := HasRefs(context.Background(), dir, "refs/heads/main")
		require.Error(t, err)
	})

	t.Run("honours context cancellation", func(t *testing.T) {
		dir := initRepo(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := HasRefs(ctx, dir, "refs/heads/main")
		require.Error(t, err)
	})

	t.Run("similarly-named refs do not bleed into each other", func(t *testing.T) {
		// Regression guard: substring matching on the show-ref output
		// would falsely match a `refs/heads/feature-x` line against a
		// `refs/heads/feature` query.
		dir := initRepo(t)
		require.NoError(t, exec.Command("git", "-C", dir, "branch", "feature-x").Run())

		found, err := HasRefs(context.Background(), dir, "refs/heads/feature")
		require.NoError(t, err)
		assert.False(t, found["refs/heads/feature"])
	})
}

func TestParseLines(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "typical multi-line for-each-ref output",
			in:   "refs/heads/main\nrefs/heads/feature\nrefs/remotes/origin/main\n",
			want: []string{"refs/heads/main", "refs/heads/feature", "refs/remotes/origin/main"},
		},
		{
			name: "trailing whitespace is trimmed",
			in:   "  main  \n  feature \n",
			want: []string{"main", "feature"},
		},
		{
			name: "interior blank lines are dropped",
			in:   "main\n\n\nfeature\n",
			want: []string{"main", "feature"},
		},
		{
			name: "no trailing newline",
			in:   "main\nfeature",
			want: []string{"main", "feature"},
		},
		{
			name: "single line",
			in:   "main",
			want: []string{"main"},
		},
		{
			name: "empty input returns nil",
			in:   "",
			want: nil,
		},
		{
			name: "only whitespace returns nil",
			in:   "   \n\t\n  ",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ParseLines(tc.in))
		})
	}
}

func TestSplitNUL(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []string
	}{
		{
			name: "typical NUL-terminated record stream",
			in:   []byte("first\x00second\x00third\x00"),
			want: []string{"first", "second", "third"},
		},
		{
			name: "no trailing NUL keeps the final record",
			in:   []byte("first\x00second"),
			want: []string{"first", "second"},
		},
		{
			name: "single record with trailing NUL",
			in:   []byte("only\x00"),
			want: []string{"only"},
		},
		{
			name: "single record without trailing NUL",
			in:   []byte("only"),
			want: []string{"only"},
		},
		{
			name: "empty input returns nil",
			in:   []byte{},
			want: nil,
		},
		{
			name: "nil input returns nil",
			in:   nil,
			want: nil,
		},
		{
			name: "interior empty records survive (only trailing-empty is dropped)",
			in:   []byte("a\x00\x00b\x00"),
			want: []string{"a", "", "b"},
		},
		{
			name: "record containing whitespace is preserved verbatim",
			in:   []byte(" leading\x00trailing \x00mid dle\x00"),
			want: []string{" leading", "trailing ", "mid dle"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SplitNUL(tc.in))
		})
	}
}

func TestStripRemotePrefix(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare local name", "main", "main"},
		{"origin-prefixed", "origin/main", "main"},
		{"upstream-prefixed", "upstream/feature", "feature"},
		{"multi-segment ref keeps suffix after first slash", "origin/feature/foo", "feature/foo"},
		{"empty string returns empty", "", ""},
		{"leading slash drops the empty segment", "/main", "main"},
		{"trailing slash returns empty suffix", "origin/", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, StripRemotePrefix(tc.in))
		})
	}
}
