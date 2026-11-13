package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newBareCheckout(t *testing.T, spelling string) (string, string) {
	t.Helper()
	source := initRepo(t)
	bare := filepath.Join(t.TempDir(), spelling)
	require.NoError(t, os.Mkdir(bare, 0o700))
	initBareRepo(t, bare)
	run(t, source, "git", "push", bare, "main")
	checkout := filepath.Join(t.TempDir(), "checkout")
	run(t, bare, "git", "--git-dir="+bare, "worktree", "add", checkout, "main")
	return pathutil.Canonicalize(bare), pathutil.Canonicalize(checkout)
}

func TestGitReproducerBareLinkedIdentity(t *testing.T) {
	for _, spelling := range []string{"project.git", "bare-storage"} {
		t.Run(spelling, func(t *testing.T) {
			bare, checkout := newBareCheckout(t, spelling)
			info, err := queryGitPathInfo(t.Context(), checkout)
			require.NoError(t, err)
			assert.True(t, info.IsWorktree)
			assert.Equal(t, bare, info.RepoRoot)
			assert.Equal(t, checkout, info.TopLevel)
		})
	}
}

func TestGitReproducerOperationalFailureIsNotAbsence(t *testing.T) {
	repo := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("[core\n"), 0o600))
	_, err := queryGitPathInfo(t.Context(), repo)
	require.Error(t, err)
	assert.NotErrorIs(t, err, errNotGitRepo)
	assert.ErrorContains(t, err, "bad config")
}

func TestGitReproducerPushConfigCancellation(t *testing.T) {
	repo := initRepo(t)
	run(t, repo, "git", "config", "remote.origin.url", "https://example.invalid/repository.git")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := pushStatusForPath(ctx, repo, "main")
	require.ErrorIs(t, err, context.Canceled)
}

func TestGitReproducerToplevelWhitespace(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("the fixture requires a Unix filename ending in whitespace")
	}
	original := initRepo(t)
	repo := filepath.Join(t.TempDir(), "repository ")
	require.NoError(t, os.Rename(original, repo))
	info, err := queryGitPathInfo(t.Context(), repo)
	require.NoError(t, err)
	assert.Equal(t, pathutil.Canonicalize(repo), info.TopLevel)
	assert.Equal(t, pathutil.Canonicalize(repo), gitutil.GetToplevelInfo(t.Context(), repo).Toplevel)
}

func TestGitReproducerNewlineAdminPaths(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("the fixture requires Unix newline filenames")
	}
	original := initRepo(t)
	repo := filepath.Join(t.TempDir(), "main\nrepository")
	require.NoError(t, os.Rename(original, repo))
	checkout := filepath.Join(t.TempDir(), "plain-checkout")
	run(t, repo, "git", "worktree", "add", "-b", "linked", checkout)
	info, err := queryGitPathInfo(t.Context(), checkout)
	require.NoError(t, err)
	assert.True(t, info.IsWorktree)
	assert.Equal(t, pathutil.Canonicalize(repo), info.RepoRoot)
	assert.Equal(t, pathutil.Canonicalize(checkout), info.TopLevel)
	assert.Equal(t, pathutil.Canonicalize(checkout), gitutil.GetToplevelInfo(t.Context(), checkout).Toplevel)
}

func TestGitReproducerMutationNeedsResolvedIdentity(t *testing.T) {
	svc, _, _ := setupTestService(t)
	called := false
	err := svc.withRepoIndexLockForDir(t.Context(), t.TempDir(), func(_ *gitPathInfo) error { called = true; return nil })
	require.Error(t, err)
	assert.False(t, called, "a failed identity lookup must not run a mutation under an unrelated fallback key")
}
