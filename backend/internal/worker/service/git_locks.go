package service

import (
	"context"
	"sync"
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
// repoRoot must be the MAIN repo root. An empty key is accepted rather than
// panicking -- it degrades to one shared lock, which over-serializes but is
// never wrong -- so a caller that failed to resolve the root still gets
// mutual exclusion instead of the raw race.
func (svc *Service) gitIndexLock(repoRoot string) *sync.Mutex {
	v, _ := svc.gitIndexLocks.LoadOrStore(repoRoot, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// lockGitIndex takes the repo's index lock and returns its release function, for
// the function-scoped holders that cannot use withGitIndexLock's closure form
// (they have no error return, or their locked region spans early returns).
//
//	defer svc.lockGitIndex(repoRoot)()
//
// Same lock, same ordering rule as withGitIndexLock -- see the LOCK ORDER note on
// gitIndexLocks. It exists so those sites are one line instead of a hand-rolled
// get/Lock/defer-Unlock triple, which is where a missing defer or a lock taken on
// the wrong key would hide.
func (svc *Service) lockGitIndex(repoRoot string) func() {
	mu := svc.gitIndexLock(repoRoot)
	mu.Lock()
	return mu.Unlock
}

// withGitIndexLock runs fn while holding repoRoot's index lock.
func (svc *Service) withGitIndexLock(repoRoot string, fn func() error) error {
	mu := svc.gitIndexLock(repoRoot)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}

// withRepoIndexLockForDir is withGitIndexLock for a caller that holds only a
// working dir. It resolves the main repo root itself, at the cost of one
// `git rev-parse` fork -- fine on the user-initiated branch dialogs that need
// it, and the reason the plan-carrying paths use withGitIndexLock instead.
//
// A resolution failure does NOT skip the lock: it falls back to the empty key,
// which serializes against every other unresolved caller. Over-serializing a
// path we cannot place is the safe direction; running it unlocked is how the
// index.lock failure happens.
func (svc *Service) withRepoIndexLockForDir(ctx context.Context, dir string, fn func() error) error {
	var repoRoot string
	if info, err := queryGitPathInfo(ctx, dir); err == nil {
		repoRoot = info.RepoRoot
	}
	return svc.withGitIndexLock(repoRoot, fn)
}
