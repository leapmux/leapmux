package testutil

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// git runs a git command in dir and returns its trimmed stdout.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// Every repo NewGitRepo hands out comes from one shared template, so the thing
// most worth pinning is that sharing the TEMPLATE does not mean sharing the
// REPO: hundreds of tests mutate these, and a leak between two of them would
// surface as an unrelated test failing somewhere else entirely.
func TestNewGitRepo_ReposAreIndependent(t *testing.T) {
	t.Parallel()

	a := NewGitRepo(t)
	b := NewGitRepo(t)
	require.NotEqual(t, a, b)

	git(t, a, "commit", "--allow-empty", "-m", "only-in-a")
	git(t, a, "branch", "branch-only-in-a")

	assert.Equal(t, "init", git(t, b, "log", "-1", "--pretty=%s"),
		"a commit in one repo must not be visible in another")
	assert.Empty(t, git(t, b, "branch", "--list", "branch-only-in-a"),
		"a branch created in one repo must not appear in another")
}

// The initial branch is pinned rather than inherited: a host without
// init.defaultBranch lands on `master`, and the worker suites address the
// branch by name (refs/heads/main, origin/main..HEAD, switch-to-main).
func TestNewGitRepo_InitialBranchIsMain(t *testing.T) {
	t.Parallel()

	dir := NewGitRepo(t)
	assert.Equal(t, "main", git(t, dir, "rev-parse", "--abbrev-ref", "HEAD"))
	assert.Equal(t, "init", git(t, dir, "log", "-1", "--pretty=%s"),
		"the repo must arrive with its initial commit already made")
}

// The background-writer settings are the reason these repos survive
// t.TempDir() cleanup. A host with core.fsmonitor on (macOS dev machines
// commonly do) makes git spawn a daemon per repo that outlives the command,
// keeps writing under .git, and races the cleanup -- which surfaces as
// "TempDir RemoveAll cleanup: ... directory not empty" against whichever test
// happened to own the dir, not against the daemon. gc/maintenance are the same
// hazard in slower motion.
//
// Asserted on the MATERIALIZED repo, not on the template build, because a `-c`
// override on `git init` would satisfy the build and leave every copy exposed.
func TestNewGitRepo_DisablesBackgroundWriters(t *testing.T) {
	t.Parallel()

	dir := NewGitRepo(t)
	for _, tc := range []struct{ key, want string }{
		{"core.fsmonitor", "false"},
		{"gc.auto", "0"},
		{"maintenance.auto", "false"},
	} {
		assert.Equal(t, tc.want, git(t, dir, "config", "--local", tc.key),
			"%s must be pinned in the repo's own config, not left to the host's", tc.key)
	}
}

// The repos are handed to tests that add worktrees, so a materialized copy has
// to be a fully working repo and not merely a directory of the right shape.
func TestNewGitRepo_SupportsWorktrees(t *testing.T) {
	t.Parallel()

	dir := NewGitRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, dir, "worktree", "add", "-b", "wt-branch", wt)

	assert.Equal(t, "wt-branch", git(t, wt, "rev-parse", "--abbrev-ref", "HEAD"))
	assert.Equal(t, "init", git(t, wt, "log", "-1", "--pretty=%s"))
}
