package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func observeNextGitIndexWait(svc *Service) <-chan struct{} {
	entered := make(chan struct{})
	var once sync.Once
	svc.beforeGitIndexLockFn = func(string) { once.Do(func() { close(entered) }) }
	return entered
}
func waitForGitIndexObservation(t *testing.T, ctx context.Context, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the operation did not resolve its index-lock identity:", ctx.Err())
	}
}
func TestCheckoutIndexLockRechecksCommonDirectoryAfterWaiting(t *testing.T) {
	svc, _, _ := setupTestService(t)
	first, second := initRepo(t), initRepo(t)
	firstAdmin, secondAdmin := filepath.Join(t.TempDir(), "admin-first"), filepath.Join(t.TempDir(), "admin-second")
	// initRepo already pins fsmonitor, gc and maintenance in the local config.
	run(t, first, "git", "init", "--separate-git-dir="+firstAdmin)
	run(t, second, "git", "init", "--separate-git-dir="+secondAdmin)
	firstInfo, err := queryGitPathInfo(t.Context(), first)
	require.NoError(t, err)
	secondInfo, err := queryGitPathInfo(t.Context(), second)
	require.NoError(t, err)
	redirect, err := os.ReadFile(filepath.Join(second, ".git"))
	require.NoError(t, err)
	before := run(t, second, "git", "symbolic-ref", "HEAD")
	firstMutex, secondMutex := svc.gitIndexLock(firstInfo.CommonDir), svc.gitIndexLock(secondInfo.CommonDir)
	firstMutex.Lock()
	secondMutex.Lock()
	var firstRelease, secondRelease sync.Once
	unlockFirst := func() { firstRelease.Do(firstMutex.Unlock) }
	unlockSecond := func() { secondRelease.Do(secondMutex.Unlock) }
	entered := observeNextGitIndexWait(svc)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	results := make(chan error, 1)
	completed := make(chan struct{})
	var called atomic.Bool
	go func() {
		defer close(completed)
		results <- svc.withRepoIndexLockForDir(ctx, first, func(_ *gitPathInfo) error {
			called.Store(true)
			return createBranchInDir(ctx, first, "must-not-create", "")
		})
	}()
	t.Cleanup(func() {
		cancel()
		unlockFirst()
		unlockSecond()
		select {
		case <-completed:
		case <-time.After(30 * time.Second):
			t.Error("the mutation did not finish during cleanup")
		}
	})
	waitForGitIndexObservation(t, ctx, entered)
	require.NoError(t, os.WriteFile(filepath.Join(first, ".git"), redirect, 0o600))
	unlockFirst()
	select {
	case err := <-results:
		assert.Error(t, err, "a changed common directory must refuse mutation")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assert.False(t, called.Load(), "repository B must not mutate while the operation holds A's mutex")
	assert.Equal(t, before, run(t, second, "git", "symbolic-ref", "HEAD"))
	refs, err := gitutil.HasRefs(t.Context(), second, "refs/heads/must-not-create")
	require.NoError(t, err)
	assert.False(t, refs["refs/heads/must-not-create"])
}
func TestCheckoutIndexLockCancellationAfterLookupSkipsMutation(t *testing.T) {
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	info, err := queryGitPathInfo(t.Context(), repo)
	require.NoError(t, err)
	mutex := svc.gitIndexLock(info.CommonDir)
	mutex.Lock()
	var release sync.Once
	unlock := func() { release.Do(mutex.Unlock) }
	entered := observeNextGitIndexWait(svc)
	ctx, cancel := context.WithCancel(t.Context())
	deadline, stop := context.WithTimeout(t.Context(), 30*time.Second)
	defer stop()
	result := make(chan error, 1)
	completed := make(chan struct{})
	var called atomic.Bool
	go func() {
		defer close(completed)
		result <- svc.withRepoIndexLockForDir(ctx, repo, func(_ *gitPathInfo) error { called.Store(true); return nil })
	}()
	t.Cleanup(func() {
		cancel()
		unlock()
		select {
		case <-completed:
		case <-time.After(30 * time.Second):
			t.Error("canceled mutation did not finish during cleanup")
		}
	})
	waitForGitIndexObservation(t, deadline, entered)
	cancel()
	unlock()
	select {
	case err := <-result:
		assert.ErrorIs(t, err, context.Canceled)
	case <-deadline.Done():
		t.Fatal(deadline.Err())
	}
	assert.False(t, called.Load(), "a canceled operation must not enter its mutation callback")
}
func TestCreatedBranchFailureDoesNotOwnAnEarlierSuccessfulBranch(t *testing.T) {
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	run(t, repo, "git", "checkout", "-b", "earlier-choice")
	run(t, repo, "git", "commit", "--allow-empty", "-m", "earlier choice")
	run(t, repo, "git", "checkout", "main")
	plan, err := svc.validateCreateBranch(t.Context(), repo, "pending-create", "")
	require.NoError(t, err)
	intent, err := svc.prepareCreationStartup(t.Context(), plan)
	require.NoError(t, err)
	mutex := svc.gitIndexLock(intent.Plan.RepoGitDir)
	mutex.Lock()
	var release sync.Once
	unlock := func() { release.Do(mutex.Unlock) }
	entered := observeNextGitIndexWait(svc)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	type creationResult struct {
		result gitModeResult
		err    error
	}
	results := make(chan creationResult, 1)
	completed := make(chan struct{})
	go func() {
		defer close(completed)
		result, err := svc.executeCreateBranch(ctx, intent.Plan)
		results <- creationResult{result, err}
	}()
	t.Cleanup(func() {
		cancel()
		unlock()
		select {
		case <-completed:
		case <-time.After(30 * time.Second):
			t.Error("pending create did not finish during cleanup")
		}
	})
	waitForGitIndexObservation(t, ctx, entered)
	// These are the branch RPCs' actual bodies, serialized by the same mutex.
	require.NoError(t, createBranchInDir(t.Context(), repo, "pending-create", ""))
	earlierRef := run(t, repo, "git", "rev-parse", "refs/heads/pending-create")
	require.NoError(t, checkoutBranchInDir(t.Context(), repo, "earlier-choice"))
	unlock()
	var result creationResult
	select {
	case result = <-results:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.Error(t, result.err)
	svc.rollbackGitMode(result.result)
	assert.Nil(t, result.result.Rollback.CreatedBranch, "a pre-existing branch cannot belong to this failed attempt")
	assert.Equal(t, "refs/heads/earlier-choice", strings.TrimSpace(run(t, repo, "git", "symbolic-ref", "HEAD")))
	ref, readErr := gitutil.Output(t.Context(), repo, "rev-parse", "--verify", "refs/heads/pending-create")
	assert.NoError(t, readErr)
	assert.Equal(t, strings.TrimSpace(earlierRef), strings.TrimSpace(ref))
}
func TestCreatedBranchRollbackPreservesALaterSuccessfulCheckout(t *testing.T) {
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	run(t, repo, "git", "branch", "later-choice")
	plan, err := svc.validateCreateBranch(t.Context(), repo, "owned-create", "")
	require.NoError(t, err)
	intent, err := svc.prepareCreationStartup(t.Context(), plan)
	require.NoError(t, err)
	result, err := svc.executeCreateBranch(t.Context(), intent.Plan)
	require.NoError(t, err)
	require.NotNil(t, result.Rollback.CreatedBranch)
	require.NoError(t, svc.withRepoIndexLockForDir(t.Context(), repo, func(_ *gitPathInfo) error { return checkoutBranchInDir(t.Context(), repo, "later-choice") }))
	svc.rollbackGitMode(result)
	assert.Equal(t, "refs/heads/later-choice", strings.TrimSpace(run(t, repo, "git", "symbolic-ref", "HEAD")))
	refs, err := gitutil.HasRefs(t.Context(), repo, "refs/heads/owned-create")
	require.NoError(t, err)
	assert.False(t, refs["refs/heads/owned-create"])
}
