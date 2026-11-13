package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAutomaticWorktreeRollback_PreservesProgressAndOtherOwners(t *testing.T) {
	for _, condition := range []string{"advanced commit", "tracked change", "untracked file", "ignored file", "other live owner", "unreadable status", "different branch", "detached checkout", "locked worktree", "initialized submodule", "unchanged", "unowned existing worktree"} {
		t.Run(condition, func(t *testing.T) {
			repo := canonicalRepoDir(t)
			require.NoError(t, os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("initial data\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("ignored.txt\n"), 0o600))
			run(t, repo, "git", "add", "tracked.txt", ".gitignore")
			run(t, repo, "git", "commit", "-m", "initial tracked data")
			if condition == "initialized submodule" {
				module := canonicalRepoDir(t)
				run(t, repo, "git", "-c", "core.fsmonitor=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "protocol.file.allow=always", "submodule", "add", module, "nested")
				configureRollbackFixtureRepository(t, filepath.Join(repo, "nested"))
				run(t, repo, "git", "commit", "-m", "add submodule")
			}
			svc, dispatcher, writer := setupTestService(t)
			branch := "rollback-safety"
			workingDir := expectedWorktreePath(t, repo, branch)
			setup := createWorktreeSetup(branch, "")
			if condition == "unowned existing worktree" {
				existing := createWorktreeForTest(t, svc, repo, branch)
				workingDir = existing.WorkingDir
				setup = useWorktreePathSetup(workingDir)
			}
			entered := make(chan terminal.Options, 1)
			release := make(chan struct{})
			var once sync.Once
			releaseStartup := func() { once.Do(func() { close(release) }) }
			svc.startTerminalFn = func(_ context.Context, options terminal.Options, _ terminal.OutputHandler, _ terminal.ExitHandler) error {
				entered <- options
				<-release
				return errors.New("controlled shell startup failure")
			}
			t.Cleanup(func() { releaseStartup(); drainAllInFlight(svc) })
			dispatch(dispatcher, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{WorkingDir: repo, GitSetup: setup, Shell: testutil.TestShell()}, writer)
			require.Empty(t, writer.errors)
			ids := collectTerminalIDs(writer)
			require.Len(t, ids, 1)
			guard, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			select {
			case options := <-entered:
				require.Equal(t, workingDir, options.WorkingDir)
			case <-guard.Done():
				t.Fatal("startup did not reach the post-phase-0 shell gate")
			}
			initial := strings.TrimSpace(run(t, workingDir, "git", "rev-parse", "HEAD"))
			row, err := svc.Queries.GetWorktreeByPath(t.Context(), workingDir)
			require.NoError(t, err)
			expectedHead := initial
			switch condition {
			case "advanced commit":
				require.NoError(t, os.WriteFile(filepath.Join(workingDir, "actor.txt"), []byte("actor's committed work\n"), 0o600))
				require.ErrorContains(t, svc.pushBranch(t.Context(), workingDir), "remote origin does not exist")
				expectedHead = strings.TrimSpace(run(t, workingDir, "git", "rev-parse", "HEAD"))
				require.NotEqual(t, initial, expectedHead)
				assert.Equal(t, "refs/heads/"+branch, strings.TrimSpace(run(t, repo, "git", "for-each-ref", "--contains", expectedHead, "--format=%(refname)", "refs/heads/")))
			case "tracked change":
				require.NoError(t, os.WriteFile(filepath.Join(workingDir, "tracked.txt"), []byte("actor's tracked edits\n"), 0o600))
			case "untracked file":
				require.NoError(t, os.WriteFile(filepath.Join(workingDir, "untracked.txt"), []byte("actor's untracked work\n"), 0o600))
			case "ignored file":
				require.NoError(t, os.WriteFile(filepath.Join(workingDir, "ignored.txt"), []byte("actor's ignored work\n"), 0o600))
				require.Empty(t, run(t, workingDir, "git", "status", "--porcelain"), "the fixture must exercise the native ignored-file limitation")
			case "other live owner":
				require.NoError(t, svc.TabPayloads.Register(t.Context(), RegisterTabPayloadParams{UserID: "other-user", TabID: "other-file", Payload: fileTabPayload(filepath.Join(workingDir, "tracked.txt"), workingDir)}))
				require.Empty(t, run(t, workingDir, "git", "status", "--porcelain"), "the other owner must protect a clean, unchanged checkout")
			case "different branch":
				run(t, workingDir, "git", "checkout", "-b", "later-branch")
			case "detached checkout":
				run(t, workingDir, "git", "checkout", "--detach", initial)
			case "locked worktree":
				run(t, repo, "git", "worktree", "lock", "--reason", "keep this worktree", workingDir)
			case "initialized submodule":
				run(t, workingDir, "git", "-c", "core.fsmonitor=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "protocol.file.allow=always", "submodule", "update", "--init")
				configureRollbackFixtureRepository(t, filepath.Join(workingDir, "nested"))
				require.Empty(t, run(t, workingDir, "git", "status", "--porcelain"), "the initialized submodule must remain clean")
			case "unreadable status":
				info, err := queryGitPathInfo(t.Context(), workingDir)
				require.NoError(t, err)
				index := filepath.Join(info.GitDir, "index")
				original, err := os.ReadFile(index)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(index, []byte("invalid index"), 0o600))
				t.Cleanup(func() {
					if _, err := os.Stat(filepath.Dir(index)); err == nil {
						_ = os.WriteFile(index, original, 0o600)
					}
				})
			}
			releaseStartup()
			waitForStartupFailure(t, svc, ids[0])
			if condition == "unchanged" {
				_, err = os.Stat(workingDir)
				assert.ErrorIs(t, err, os.ErrNotExist)
				assert.False(t, localBranchExists(t, repo, branch))
				stored, err := svc.Queries.GetWorktreeByID(t.Context(), row.ID)
				require.NoError(t, err)
				assert.True(t, stored.DeletedAt.Valid)
				return
			}
			_, err = os.Stat(workingDir)
			require.NoError(t, err, "automatic startup cleanup must preserve this worktree")
			assert.Equal(t, expectedHead, strings.TrimSpace(run(t, repo, "git", "rev-parse", "refs/heads/"+branch)))
			stored, err := svc.Queries.GetWorktreeByID(t.Context(), row.ID)
			require.NoError(t, err)
			assert.False(t, stored.DeletedAt.Valid, "preserved artifacts must retain their tracking identity")
			if condition == "different branch" {
				assert.Equal(t, "refs/heads/later-branch", strings.TrimSpace(run(t, workingDir, "git", "symbolic-ref", "HEAD")))
			}
			if condition == "detached checkout" {
				assert.Equal(t, "HEAD", strings.TrimSpace(run(t, workingDir, "git", "rev-parse", "--abbrev-ref", "HEAD")))
			}
			if condition == "advanced commit" {
				assert.Equal(t, "actor's committed work\n", run(t, repo, "git", "show", "refs/heads/"+branch+":actor.txt"))
			}
			if condition == "tracked change" {
				data, err := os.ReadFile(filepath.Join(workingDir, "tracked.txt"))
				require.NoError(t, err)
				assert.Equal(t, "actor's tracked edits\n", string(data))
			}
			if condition == "untracked file" {
				data, err := os.ReadFile(filepath.Join(workingDir, "untracked.txt"))
				require.NoError(t, err)
				assert.Equal(t, "actor's untracked work\n", string(data))
			}
			if condition == "ignored file" {
				data, err := os.ReadFile(filepath.Join(workingDir, "ignored.txt"))
				require.NoError(t, err)
				assert.Equal(t, "actor's ignored work\n", string(data))
			}
		})
	}
}

func configureRollbackFixtureRepository(t *testing.T, directory string) {
	t.Helper()
	for _, setting := range []struct{ key, value string }{{"core.fsmonitor", "false"}, {"gc.auto", "0"}, {"maintenance.auto", "false"}} {
		run(t, directory, "git", "config", setting.key, setting.value)
	}
}

func TestAutomaticWorktreeRollback_RechecksInterruptionAfterIndexWait(t *testing.T) {
	for _, scenario := range []struct {
		interruption string
		phase0       bool
		preserve     bool
	}{
		{"keep", true, true},
		{"keep", false, true},
		{"archive", true, true},
		{"shutdown", true, true},
		{"archive", false, false},
		{"shutdown", false, false},
		{"none", true, false},
	} {
		t.Run(fmt.Sprintf("%s/phase0=%t", scenario.interruption, scenario.phase0), func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			repo := canonicalRepoDir(t)
			result := createWorktreeForTest(t, svc, repo, "late-interruption")
			rollback := *result.Rollback.CreatedWorktree
			registry := &svc.TerminalStartup.startupCore
			entry := beginForTest(t, registry, "rollback-startup")
			if scenario.phase0 {
				registry.markPhase0Complete(entry)
			}
			rollback.startup = worktreeRollbackStartup{
				key:      newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_TERMINAL, entry.id, ""),
				registry: registry, entry: entry,
			}
			mutex := svc.gitIndexLock(rollback.RepoGitDir)
			mutex.Lock()
			var once sync.Once
			release := func() { once.Do(mutex.Unlock) }
			entered := observeNextGitIndexWait(svc)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				svc.rollbackCreatedWorktree(rollback)
			}()
			t.Cleanup(func() {
				release()
				select {
				case <-finished:
				case <-time.After(30 * time.Second):
					t.Error("automatic rollback did not finish after releasing the index lock")
				}
			})
			guard := testutil.DeadlineContext(t)
			waitForGitIndexObservation(t, guard, entered)
			switch scenario.interruption {
			case "keep":
				registry.cancelAndClear(entry.id, keepWorktreeOnClose)
			case "archive":
				registry.cancelForArchive(entry.id)
			case "shutdown":
				registry.cancelForShutdown()
			}
			release()
			select {
			case <-finished:
			case <-guard.Done():
				t.Fatal("automatic rollback did not finish:", guard.Err())
			}
			_, err := os.Stat(result.WorkingDir)
			if scenario.preserve {
				require.NoError(t, err, "a lifecycle decision recorded during the wait must preserve its worktree")
				assert.True(t, localBranchExists(t, repo, rollback.BranchName))
				stored, err := svc.Queries.GetWorktreeByID(t.Context(), result.WorktreeID)
				require.NoError(t, err)
				assert.False(t, stored.DeletedAt.Valid)
			} else {
				assert.ErrorIs(t, err, os.ErrNotExist)
				assert.False(t, localBranchExists(t, repo, rollback.BranchName))
			}
		})
	}
}

func TestAutomaticWorktreeRollback_PreservesUnprovedOrReplacedTracking(t *testing.T) {
	for _, condition := range []string{"missing commit", "replaced tracking"} {
		t.Run(condition, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			repo := canonicalRepoDir(t)
			result := createWorktreeForTest(t, svc, repo, "proof-required")
			rollback := *result.Rollback.CreatedWorktree
			retainedID := result.WorktreeID
			if condition == "missing commit" {
				rollback.initialBranchCommit = ""
			} else {
				require.NoError(t, svc.Queries.DeleteWorktree(t.Context(), result.WorktreeID))
				var err error
				retainedID, err = svc.ensureTrackedWorktree(t.Context(), result.WorkingDir)
				require.NoError(t, err)
				require.NotEqual(t, result.WorktreeID, retainedID)
			}
			svc.rollbackCreatedWorktree(rollback)
			_, err := os.Stat(result.WorkingDir)
			require.NoError(t, err)
			assert.True(t, localBranchExists(t, repo, rollback.BranchName))
			stored, err := svc.Queries.GetWorktreeByPath(t.Context(), result.WorkingDir)
			require.NoError(t, err)
			assert.Equal(t, retainedID, stored.ID)
		})
	}
}

func TestAutomaticWorktreeRollback_CleansAnUntrackedOwnedCreation(t *testing.T) {
	svc, _, _ := setupTestService(t)
	repo := canonicalRepoDir(t)
	plan, err := svc.validateCreateWorktree(t.Context(), repo, "not-yet-tracked", "")
	require.NoError(t, err)
	initial := strings.TrimSpace(run(t, repo, "git", "rev-parse", "--verify", plan.StartPoint+"^{commit}"))
	run(t, repo, "git", "worktree", "add", "-b", plan.BranchName, plan.WorktreePath, initial)
	_, err = svc.Queries.GetWorktreeByPath(t.Context(), plan.WorktreePath)
	require.ErrorIs(t, err, sql.ErrNoRows)
	svc.rollbackCreatedWorktree(rollbackWorktree{
		WorktreePath: plan.WorktreePath, RepoRoot: plan.RepoRoot, RepoGitDir: plan.RepoGitDir,
		BranchName: plan.BranchName, initialBranchCommit: initial,
	})
	_, err = os.Stat(plan.WorktreePath)
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.False(t, localBranchExists(t, repo, plan.BranchName))
	_, err = svc.Queries.GetWorktreeByPath(t.Context(), plan.WorktreePath)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}
