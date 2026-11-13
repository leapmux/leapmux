package service

import (
	"context"
	"errors"
	"sync"

	"github.com/leapmux/leapmux/util/pathutil"
)

// The worker's git serialization locks, kept beside the git code they guard
// (git.go, worktree.go) rather than in service.go, which had accreted every
// family's shared plumbing.
//
// Two levels, and the ORDER between them is load-bearing -- see the notes on the
// Service fields they read.

// worktreeRemovalLock returns the per-worktree mutex that serializes the
// count-then-remove critical section in closeTabCommon.
func (svc *Service) worktreeRemovalLock(worktreeID string) *sync.Mutex {
	v, _ := svc.worktreeRemovalLocks.LoadOrStore(worktreeID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// gitIndexLock returns the per-repository mutex that serializes index-mutating
// git commands. See the note on gitIndexLocks, including its lock order.
//
// repoGitDir is the canonical common administrative directory. An empty key
// identifies no resolved repository. Mutation callers resolve identity before
// they enter this helper.
//
// The key is CANONICALIZED here rather than at the call sites. One repo has
// more than one spelling -- `/var/...` and `/private/var/...` name the same
// directory on macOS, where $TMPDIR is a symlink -- and callers reach this
// with whichever spelling their plan happened to carry. Two spellings meant
// two mutexes, so two `git worktree add`s in one repository ran concurrently
// and the loser died on `Unable to create '.git/worktrees/<branch>/index.lock':
// File exists` -- the exact failure this lock exists to prevent, reintroduced
// by the lock silently not being the same lock. Doing it here makes the
// mistake unavailable to callers instead of asking each of them to remember.
func (svc *Service) gitIndexLock(repoGitDir string) *sync.Mutex {
	v, _ := svc.gitIndexLocks.LoadOrStore(pathutil.Canonicalize(repoGitDir), &sync.Mutex{})
	return v.(*sync.Mutex)
}

// lockGitIndex takes the repo's index lock and returns its release function, for
// the function-scoped holders that cannot use withGitIndexLock's closure form
// (they have no error return, or their locked region spans early returns).
//
//	defer svc.lockGitIndex(repoGitDir)()
//
// Same lock, same ordering rule as withGitIndexLock -- see the LOCK ORDER note on
// gitIndexLocks. It exists so those sites are one line instead of a hand-rolled
// get/Lock/defer-Unlock triple, which is where a missing defer or a lock taken on
// the wrong key would hide.
func (svc *Service) lockGitIndex(repoGitDir string) func() {
	mu := svc.gitIndexLock(repoGitDir)
	if svc.beforeGitIndexLockFn != nil {
		svc.beforeGitIndexLockFn(repoGitDir)
	}
	mu.Lock()
	return mu.Unlock
}

// withGitIndexLock runs fn while holding repoGitDir's index lock.
func (svc *Service) withGitIndexLock(repoGitDir string, fn func() error) error {
	defer svc.lockGitIndex(repoGitDir)()
	return fn()
}

// withRepoIndexLockForDir resolves the mutex key, then revalidates the selected
// checkout after waiting. The callback receives only protected current metadata.
func (svc *Service) withRepoIndexLockForDir(ctx context.Context, dir string, fn func(*gitPathInfo) error) error {
	captured, err := queryGitPathInfo(ctx, dir)
	if err != nil {
		return err
	}
	return svc.withGitIndexLock(captured.CommonDir, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := queryGitPathInfo(ctx, dir)
		if err != nil {
			return err
		}
		if !pathutil.SamePath(current.CommonDir, captured.CommonDir) {
			return errors.New("git repository changed while waiting for index lock")
		}
		return fn(current)
	})
}
