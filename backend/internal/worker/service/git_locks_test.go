package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGitIndexLock_SameRepoThroughASymlinkIsOneLock pins that the index lock
// identifies a REPOSITORY, not a string.
//
// TestGitIndexLock_ExcludesSameRepoOnly already covers the primitive's mutual
// exclusion and its per-repo scoping; what it cannot see is that one repo root
// has more than one spelling. On macOS $TMPDIR is a symlink, so the same
// directory reaches this as both /var/... and /private/var/..., and callers
// hand it whichever their git-mode plan happened to carry. Keyed on the raw
// string those were two mutexes, so two `git worktree add`s in one repository
// ran concurrently and the loser died on
// `Unable to create '.git/worktrees/<branch>/index.lock': File exists` -- the
// exact failure the lock exists to prevent, reintroduced by the lock quietly
// not being the same lock.
func TestGitIndexLock_SameRepoThroughASymlinkIsOneLock(t *testing.T) {
	t.Parallel()

	svc := &Service{}

	realDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(realDir, 0o755))
	linkDir := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("filesystem rejected symlink: %v", err)
	}

	assert.Same(t, svc.gitIndexLock(realDir), svc.gitIndexLock(linkDir),
		"both spellings of one repo root must resolve to the same mutex")

	// Prove it excludes, not merely that the pointers match: hold the lock under
	// one spelling and the other spelling must wait for it.
	release := svc.lockGitIndex(realDir)
	entered := make(chan struct{})
	go func() {
		defer close(entered)
		svc.lockGitIndex(linkDir)()
	}()
	select {
	case <-entered:
		t.Fatal("the symlinked spelling took the lock while the real one held it; git commands in that repo can still collide on index.lock")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the waiter never acquired the lock after it was released")
	}
}
