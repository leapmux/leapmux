package service

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/pathutil"
)

// Extra coverage for the git-mode validate/execute split. These tests
// exercise the happy paths for each mode plus some edge cases the open_*
// RPC-level tests don't cover (direct validate/execute calls so we can
// inspect the gitModePlan, branch-with-slash names, remote base branches,
// and the phase/rollback label renderers).

// ---------- validateGitMode: happy paths ----------

func TestValidateGitMode_CreateWorktreeHappyPath(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, _, _ := setupTestService(t)

	// An empty base branch means the current branch, or HEAD when detached.
	plan, err := svc.validateGitMode(context.Background(), repoDir, createWorktreeSetup("feature/deep/slash", ""))
	require.NoError(t, err)
	assert.Equal(t, gitModeCreateWorktree, plan.Mode)
	assert.Equal(t, "feature/deep/slash", plan.BranchName)
	// Path prefix/suffix only — repoRoot is canonicalized via
	// pathutil.Canonicalize, so on macOS /var resolves to /private/var.
	// Normalize to forward slashes so the same literal suffix matches on
	// Windows where filepath.Join produces backslash-separated paths.
	assert.True(t, strings.HasSuffix(filepath.ToSlash(plan.WorktreePath), "-worktrees/feature/deep/slash"),
		"worktree path should live under <repo>-worktrees/<branch>: %s", plan.WorktreePath)
	assert.Equal(t, plan.WorktreePath, plan.PlannedWorkingDir, "planned working dir == worktree path")
	assert.NotEmpty(t, plan.StartPoint, "start point should default to HEAD or current branch")
}

func TestValidateGitMode_CreateBranchHappyPath(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, _, _ := setupTestService(t)

	plan, err := svc.validateGitMode(context.Background(), repoDir, createBranchSetup("feature/fresh", ""))
	require.NoError(t, err)
	assert.Equal(t, gitModeCreateBranch, plan.Mode)
	assert.Equal(t, "feature/fresh", plan.BranchName)
	assert.Equal(t, repoDir, plan.PlannedWorkingDir, "create-branch does not change working dir")
}

func TestValidateGitMode_CheckoutExistingLocalBranch(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/local")
	run(t, repoDir, "git", "checkout", "-")
	svc, _, _ := setupTestService(t)

	plan, err := svc.validateGitMode(context.Background(), repoDir, checkoutBranchSetup("feature/local"))
	require.NoError(t, err)
	assert.Equal(t, gitModeCheckoutBranch, plan.Mode)
	assert.Equal(t, "feature/local", plan.CheckoutTarget)
}

func TestValidateGitMode_CheckoutRemoteRef(t *testing.T) {
	t.Parallel()

	local := initRepo(t)
	remote := initRepo(t)
	run(t, remote, "git", "checkout", "-b", "feature/remote")
	run(t, remote, "git", "commit", "--allow-empty", "-m", "c")
	run(t, local, "git", "remote", "add", "origin", remote)
	run(t, local, "git", "fetch", "origin")
	svc, _, _ := setupTestService(t)

	plan, err := svc.validateGitMode(context.Background(), local, checkoutBranchSetup("origin/feature/remote"))
	require.NoError(t, err)
	assert.Equal(t, gitModeCheckoutBranch, plan.Mode)
}

func TestValidateGitMode_UseCurrentDefault(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, _, _ := setupTestService(t)

	plan, err := svc.validateGitMode(context.Background(), repoDir, nil)
	require.NoError(t, err)
	assert.Equal(t, gitModeUseCurrent, plan.Mode)
	assert.Equal(t, repoDir, plan.PlannedWorkingDir)
	assert.Empty(t, plan.PhaseLabel(), "useCurrent has no user-visible phase label")
}

func TestValidateGitMode_CreateWorktreeWithRemoteBaseBranch(t *testing.T) {
	t.Parallel()

	local := initRepo(t)
	remote := initRepo(t)
	run(t, remote, "git", "checkout", "-b", "feature/base")
	run(t, remote, "git", "commit", "--allow-empty", "-m", "c")
	run(t, local, "git", "remote", "add", "origin", remote)
	run(t, local, "git", "fetch", "origin")
	svc, _, _ := setupTestService(t)

	plan, err := svc.validateGitMode(context.Background(), local, createWorktreeSetup("feature/new-from-remote", "origin/feature/base"))
	require.NoError(t, err)
	assert.Equal(t, "origin/feature/base", plan.StartPoint)
}

// ---------- executeGitMode: happy paths ----------

func TestExecuteGitMode_CreateWorktreeSucceeds(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, _, _ := setupTestService(t)

	plan, err := svc.validateGitMode(context.Background(), repoDir, createWorktreeSetup("feature/exec-happy", ""))
	require.NoError(t, err)

	res, err := svc.executeGitMode(context.Background(), plan)
	require.NoError(t, err)
	assert.DirExists(t, res.WorkingDir)
	assert.NotEmpty(t, res.WorktreeID)
	assert.NotNil(t, res.Rollback.CreatedWorktree)
	assert.Equal(t, "feature/exec-happy", res.Rollback.CreatedWorktree.BranchName)
}

func TestExecuteGitMode_CreateBranchSucceeds(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, _, _ := setupTestService(t)

	plan, err := svc.validateGitMode(context.Background(), repoDir, createBranchSetup("feature/exec-branch", ""))
	require.NoError(t, err)

	res, err := svc.executeGitMode(context.Background(), plan)
	require.NoError(t, err)
	assert.Equal(t, repoDir, res.WorkingDir)
	assert.True(t, localBranchExists(t, repoDir, "feature/exec-branch"))
	require.NotNil(t, res.Rollback.CreatedBranch, "execute should expose rollback metadata so a downstream failure can reset HEAD")
	assert.Equal(t, "feature/exec-branch", res.Rollback.CreatedBranch.CreatedBranch)
}

// TestExecuteCreateBranch_BranchNameErrorSkipsRollback pins the
// regression where executeCreateBranch always returned Rollback.
// CreatedBranch even when createBranchInDir's own ValidateBranchName
// aborted before any `git checkout -b` ran. The caller's
// HasPartialMutation then ran `git branch -D <name>` on a ref that
// never existed, emitting a misleading "rolling back branch" label and
// Warn log. With the fix, BranchNameError aborts cleanly with no
// rollback metadata.
func TestExecuteCreateBranch_BranchNameErrorSkipsRollback(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, _, _ := setupTestService(t)

	plan := gitModePlan{
		Mode:              gitModeCreateBranch,
		WorkingDir:        repoDir,
		PlannedWorkingDir: repoDir,
		BranchName:        "has space", // rejected by ValidateBranchName before git is invoked
	}
	res, err := svc.executeCreateBranch(context.Background(), plan)
	require.Error(t, err)
	assert.True(t, gitutil.IsBranchNameError(err), "expected BranchNameError, got %T: %v", err, err)
	assert.Nil(t, res.Rollback.CreatedBranch,
		"BranchNameError aborted before git ran — no rollback should be requested")
}

// An unused, unchanged owned branch can be removed without restoring an
// unavailable original checkout. The later successful checkout stays selected.
func TestRollbackCreatedBranch_RemovesAnUnusedUnchangedBranch(t *testing.T) {
	t.Parallel()
	repoDir := initRepo(t)
	svc, _, _ := setupTestService(t)
	run(t, repoDir, "git", "checkout", "-b", "leftover")
	initial := strings.TrimSpace(run(t, repoDir, "git", "rev-parse", "HEAD"))
	run(t, repoDir, "git", "checkout", "-")
	info, err := queryGitPathInfo(t.Context(), repoDir)
	require.NoError(t, err)
	svc.rollbackCreatedBranch(rollbackBranch{
		RepoRoot: info.RepoRoot, RepoGitDir: info.CommonDir, WorkingDir: repoDir,
		OriginalBranch: "ghost-original-that-does-not-exist", CreatedBranch: "leftover",
		initialBranchCommit: initial,
	})
	assert.False(t, localBranchExists(t, repoDir, "leftover"))
	assert.Equal(t, "refs/heads/main", strings.TrimSpace(run(t, repoDir, "git", "symbolic-ref", "HEAD")))
}

// ---------- labels ----------

func TestGitModePlan_PhaseLabelPerMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		plan gitModePlan
		want string
	}{
		{gitModePlan{Mode: gitModeCreateWorktree, BranchName: "feature/x"}, `Creating worktree "feature/x"…`},
		// The label for an existing worktree shows the last path component.
		{gitModePlan{Mode: gitModeUseWorktree, WorktreePath: "/tmp/foo-worktrees/feature/bar"}, `Switching to worktree "bar"…`},
		{gitModePlan{Mode: gitModeCreateBranch, BranchName: "feature/y"}, `Creating branch "feature/y"…`},
		{gitModePlan{Mode: gitModeCheckoutBranch, CheckoutTarget: "main"}, `Switching to branch "main"…`},
		{gitModePlan{Mode: gitModeUseCurrent}, ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, c.plan.PhaseLabel(), "mode=%d", c.plan.Mode)
	}
}

func TestRollbackLabelFromRollback_OnlyForMutatingModes(t *testing.T) {
	t.Parallel()

	// Only createWorktree / createBranch mutate state that the user cares
	// about rolling back; the other modes produce an empty label so the
	// startup goroutine skips the pre-failure broadcast.
	wtLabel := rollbackLabelFromRollback(gitModeRollback{CreatedWorktree: &rollbackWorktree{BranchName: "feature/x"}})
	assert.Equal(t, `Rolling back worktree "feature/x"…`, wtLabel)
	brLabel := rollbackLabelFromRollback(gitModeRollback{CreatedBranch: &rollbackBranch{CreatedBranch: "feature/y"}})
	assert.Equal(t, `Rolling back branch "feature/y"…`, brLabel)
	assert.Empty(t, rollbackLabelFromRollback(gitModeRollback{}))
}

// ---------- validation edge cases ----------

func TestValidateGitMode_WorktreePathNormalization(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	worktreeDir := filepath.Join(t.TempDir(), "feature", "deep", "nest")
	run(t, repoDir, "git", "worktree", "add", "-b", "feature/deep/nest", worktreeDir)
	alias := filepath.Join(t.TempDir(), "symlink")
	require.NoError(t, os.Symlink(worktreeDir, alias))
	svc, _, _ := setupTestService(t)

	plan, err := svc.validateGitMode(t.Context(), repoDir, useWorktreePathSetup(alias))
	require.NoError(t, err)
	assert.Equal(t, gitModeUseWorktree, plan.Mode)
	assert.Equal(t, pathutil.Canonicalize(worktreeDir), plan.PlannedWorkingDir)
	assert.Equal(t, plan.PlannedWorkingDir, plan.WorktreePath)

	replacement := filepath.Join(t.TempDir(), "other")
	run(t, repoDir, "git", "worktree", "add", "-b", "other", replacement)
	require.NoError(t, os.Remove(alias))
	require.NoError(t, os.Symlink(replacement, alias))
	result, err := svc.executeGitMode(t.Context(), plan)
	require.NoError(t, err)
	tracked, err := svc.Queries.GetWorktreeByID(t.Context(), result.WorktreeID)
	require.NoError(t, err)
	assert.Equal(t, pathutil.Canonicalize(worktreeDir), result.WorkingDir)
	assert.Equal(t, result.WorkingDir, tracked.WorktreePath)
}

func TestExecuteGitModeRejectsMovedTrackedWorktree(t *testing.T) {
	t.Parallel()
	repoDir := initRepo(t)
	worktreeParent := t.TempDir()
	worktreeDir := filepath.Join(worktreeParent, "original")
	movedDir := filepath.Join(worktreeParent, "moved")
	run(t, repoDir, "git", "worktree", "add", "-b", "feature/move", worktreeDir)
	svc, _, _ := setupTestService(t)
	plan, err := svc.validateGitMode(t.Context(), repoDir, useWorktreePathSetup(worktreeDir))
	require.NoError(t, err)
	first, err := svc.executeGitMode(t.Context(), plan)
	require.NoError(t, err)
	require.NotEmpty(t, first.WorktreeID)

	run(t, repoDir, "git", "worktree", "move", worktreeDir, movedDir)
	result, err := svc.executeGitMode(t.Context(), plan)
	require.ErrorIs(t, err, errSelectedWorktreeChanged)
	assert.Empty(t, result.WorktreeID)
	assert.DirExists(t, movedDir)
	assert.NoDirExists(t, worktreeDir)
}

func TestSavedWorktreeRejectsADifferentCheckoutAtTheSamePath(t *testing.T) {
	t.Parallel()
	repoDir := initRepo(t)
	parent := t.TempDir()
	originalDir := filepath.Join(parent, "original")
	replacementDir := filepath.Join(parent, "replacement")
	movedDir := filepath.Join(parent, "moved")
	run(t, repoDir, "git", "worktree", "add", "-b", "original", originalDir)
	run(t, repoDir, "git", "worktree", "add", "-b", "replacement", replacementDir)
	svc, _, _ := setupTestService(t)
	plan, err := svc.validateGitMode(t.Context(), repoDir, useWorktreePathSetup(originalDir))
	require.NoError(t, err)
	result, err := svc.executeGitMode(t.Context(), plan)
	require.NoError(t, err)
	op := &creationOperation{coordinator: &tabCreationCoordinator{svc: svc}}
	intent := &creationStartupIntent{Plan: plan, Result: &result}
	require.NoError(t, op.validateSavedCheckpoint(t.Context(), intent, result))
	_, err = plan.readSelectedWorktree(t.Context())
	require.NoError(t, err)

	run(t, repoDir, "git", "worktree", "move", originalDir, movedDir)
	run(t, repoDir, "git", "worktree", "move", replacementDir, originalDir)
	require.ErrorIs(t, op.validateSavedCheckpoint(t.Context(), intent, result), errSelectedWorktreeChanged)
	_, err = plan.readSelectedWorktree(t.Context())
	require.ErrorIs(t, err, errSelectedWorktreeChanged)
	assert.DirExists(t, originalDir)
	assert.DirExists(t, movedDir)
}

func TestSavedWorktreeRejectsARecreatedCheckout(t *testing.T) {
	t.Parallel()
	repoDir := initRepo(t)
	worktreeDir := filepath.Join(t.TempDir(), "checkout")
	run(t, repoDir, "git", "worktree", "add", "-b", "recreated", worktreeDir)
	svc, _, _ := setupTestService(t)
	plan, err := svc.validateGitMode(t.Context(), repoDir, useWorktreePathSetup(worktreeDir))
	require.NoError(t, err)
	if !plan.SelectedWorktree.CheckoutID.known() || !plan.SelectedWorktree.GitDirID.known() {
		t.Skip("the filesystem reports no creation identity for the test directories")
	}
	_, err = plan.readSelectedWorktree(t.Context())
	require.NoError(t, err)
	result, err := svc.executeGitMode(t.Context(), plan)
	require.NoError(t, err)
	tracked, err := svc.Queries.GetWorktreeByID(t.Context(), result.WorktreeID)
	require.NoError(t, err)
	unlock := svc.lockGitIndex(tracked.RepoGitDir)
	identity, err := svc.captureWorktreeRemovalIdentityLocked(t.Context(), tracked,
		newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_TERMINAL, "terminal", "user-1"), plan.SelectedWorktree)
	unlock()
	require.NoError(t, err)
	t.Cleanup(func() { releaseRemovalIdentityBestEffort(identity) })
	_, err = readVerifiedRemovalWorktree(t.Context(), tracked, identity)
	require.NoError(t, err)

	run(t, repoDir, "git", "worktree", "remove", worktreeDir)
	run(t, repoDir, "git", "worktree", "add", worktreeDir, "recreated")
	info, err := queryGitPathInfo(t.Context(), worktreeDir)
	require.NoError(t, err)
	require.Equal(t, plan.SelectedWorktree.GitDir, info.GitDir)
	_, err = plan.readSelectedWorktree(t.Context())
	require.ErrorIs(t, err, errSelectedWorktreeChanged)
	// Model a replacement arriving before the marker write reaches Git's directory.
	require.NoError(t, os.WriteFile(filepath.Join(info.GitDir, identity.MarkerName), []byte(identity.Token), 0o600))
	_, err = readVerifiedRemovalWorktree(t.Context(), tracked, identity)
	require.ErrorIs(t, err, errSelectedWorktreeChanged)
	assert.DirExists(t, worktreeDir)
}

func TestValidateGitModePreservesSignificantWorktreePathSpace(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows path normalization does not preserve a trailing space")
	}
	repoDir := initRepo(t)
	parent := t.TempDir()
	ordinary := filepath.Join(parent, "checkout")
	withSpace := filepath.Join(parent, "checkout ")
	run(t, repoDir, "git", "worktree", "add", "-b", "ordinary", ordinary)
	run(t, repoDir, "git", "worktree", "add", "-b", "with-space", withSpace)
	svc, _, _ := setupTestService(t)
	plan, err := svc.validateGitMode(t.Context(), repoDir, useWorktreePathSetup(withSpace))
	require.NoError(t, err)
	assert.Equal(t, pathutil.Canonicalize(withSpace), plan.WorktreePath)
}

func TestCloseTerminalKeepsTheReplacementForASelectedWorktree(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		dropLink bool
		archived bool
	}{
		{name: "stored link"},
		{name: "path fallback", dropLink: true},
		{name: "persisted archive", archived: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			clock := testutil.NewQuartzMock(t)
			resizeStart, resizeStop := testutil.NewTimerTraps(t, clock, startupPendingResizeTimerTag)
			svc, dispatcher, writer := setupTestService(t, withClock(clock))
			spawned := make(chan struct{}, 1)
			svc.startTerminalFn = func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
				spawned <- struct{}{}
				return nil
			}
			repoDir := initRepo(t)
			parent := t.TempDir()
			original := filepath.Join(parent, "original")
			moved := filepath.Join(parent, "moved")
			replacement := filepath.Join(parent, "replacement")
			run(t, repoDir, "git", "worktree", "add", "-b", "original", original)
			run(t, repoDir, "git", "worktree", "add", "-b", "replacement", replacement)
			dispatch(dispatcher, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
				WorkingDir: repoDir, Shell: testutil.TestShell(), Cols: 80, Rows: 24, GitSetup: useWorktreePathSetup(original),
			}, writer)
			require.Empty(t, writer.errors)
			require.Len(t, writer.responses, 1)
			var opened leapmuxv1.OpenTerminalResponse
			require.NoError(t, proto.Unmarshal(writer.responses[0].GetPayload(), &opened))
			terminalID := opened.GetTerminalId()
			delay := testutil.WaitForTimer(t, ctx, resizeStart)
			run(t, repoDir, "git", "worktree", "move", original, moved)
			run(t, repoDir, "git", "worktree", "move", replacement, original)
			if test.archived {
				// The archive row commits before the startup registry gets its stamp.
				_, err := svc.DB.ExecContext(ctx, "UPDATE terminals SET workspace_archived = 1 WHERE id = ?", terminalID)
				require.NoError(t, err)
			}
			testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, resizeStop)
			drainAllInFlight(svc)
			require.Empty(t, spawned, "the selected checkout changed before spawn")
			row, err := svc.Queries.GetTerminal(ctx, terminalID)
			require.NoError(t, err)
			if test.archived {
				require.Empty(t, row.StartupError)
				return
			}
			require.Contains(t, row.StartupError, errSelectedWorktreeChanged.Error())
			testutil.RegisterTerminalCleanup(t, svc.Terminals, terminalID)
			restarted := newTestWriter()
			dispatch(dispatcher, leapmuxv1connect.WorkerTerminalServiceRestartTerminalProcedure, &leapmuxv1.RestartTerminalRequest{
				TerminalId: terminalID, Cols: 80, Rows: 24,
			}, restarted)
			require.Empty(t, restarted.errors)
			drainAllInFlight(svc)
			assert.False(t, svc.Terminals.IsRunning(terminalID), "restart must keep the selected checkout identity")
			row, err = svc.Queries.GetTerminal(ctx, terminalID)
			require.NoError(t, err)
			assert.Contains(t, row.StartupError, errSelectedWorktreeChanged.Error())
			if test.dropLink {
				require.NoError(t, svc.unregisterTabContext(ctx, leapmuxv1.TabType_TAB_TYPE_TERMINAL, terminalID, ""))
			}
			closed := newTestWriter()
			dispatch(dispatcher, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
				Tab: terminalTabIdent(terminalID), WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
			}, closed)
			require.Empty(t, closed.errors)
			require.Len(t, closed.responses, 1)
			var result leapmuxv1.CloseTabResponse
			require.NoError(t, proto.Unmarshal(closed.responses[0].GetPayload(), &result))
			assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, result.GetWorktreeRemoval())
			assert.Contains(t, result.GetFailureDetail(), "selected Git worktree changed")
			assert.DirExists(t, original)
			assert.True(t, localBranchExists(t, repoDir, "replacement"))
			row, err = svc.Queries.GetTerminal(ctx, terminalID)
			require.NoError(t, err)
			assert.True(t, row.ClosedAt.Valid)
		})
	}
}

func TestPendingSelectedWorktreeCloseRefusesForeignTracking(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, dispatcher, _ := setupTestService(t)
	originalRepo, selectedRepo := initRepo(t), initRepo(t)
	parent := t.TempDir()
	path, moved := filepath.Join(parent, "checkout"), filepath.Join(parent, "moved")
	run(t, originalRepo, "git", "worktree", "add", "-b", "original", path)
	_, err := svc.ensureTrackedWorktree(ctx, path)
	require.NoError(t, err)
	run(t, originalRepo, "git", "worktree", "move", path, moved)
	run(t, selectedRepo, "git", "worktree", "add", "-b", "selected", path)

	const terminalID = "pending-selected-worktree"
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	svc.PublishTab = func(ctx context.Context, publication *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
		close(entered)
		select {
		case <-release:
			return fixturePublishTab(ctx, publication)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	writer := newTestWriter()
	go func() {
		defer close(done)
		dispatch(dispatcher, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
			Target:     &leapmuxv1.OpenTerminalRequest_Creation{Creation: creationIntent(terminalID)},
			WorkingDir: selectedRepo, Shell: testutil.TestShell(), GitSetup: useWorktreePathSetup(path),
		}, writer)
	}()
	t.Cleanup(func() { close(release); <-done; drainAllInFlight(svc) })
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the Worker did not reach publication")
	}
	snapshot := svc.prepareTabClose(ctx, leapmuxv1.TabType_TAB_TYPE_TERMINAL, terminalID, "user-1",
		leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink, func() {})
	assert.False(t, snapshot.LookupFailed, "a different tracked checkout is a definitive refusal")
	assert.Equal(t, leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP, snapshot.Action)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, snapshot.Result.GetWorktreeRemoval())
	assert.Nil(t, snapshot.RemovalIdentity)
	assert.DirExists(t, path)
	assert.DirExists(t, moved)
}

// ---------- End-to-end happy paths ----------
//
// These tests exercise the full OpenAgent pipeline for each git-mode to
// prove that validate → DB insert → async execute → post-phase-0 state
// all line up. They assert:
//   - OpenAgent returns OK in <200 ms (sync prologue only validates).
//   - The async goroutine lands the tab in the expected post-exec state
//     (worktree created, branch created, etc.).
//   - No stray connect errors surface.

func TestOpenAgent_CreateWorktree_EndToEnd(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return map[string]string{}, nil
	}

	repoDir := initRepo(t)
	branch := "feature/e2e-worktree"
	dispatch(d, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{
		WorkingDir:    repoDir,
		GitSetup:      createWorktreeSetup(branch, ""),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()

	// Phase 0 eventually creates the worktree and registers it.
	require.Eventually(t, func() bool {
		return directoryExists(expectedWorktreePath(t, repoDir, branch))
	}, 5*time.Second, 20*time.Millisecond, "expected worktree to be created")
	assert.True(t, localBranchExists(t, repoDir, branch))

	// The agent's DB working_dir points at the worktree.
	row, err := svc.Queries.GetAgentByID(context.Background(), agentID)
	require.NoError(t, err)
	// Normalize to forward slashes so the branch-name suffix matches on
	// Windows where the worktree path uses backslash separators.
	assert.True(t, strings.HasSuffix(filepath.ToSlash(row.WorkingDir), branch),
		"DB row's working_dir should be the worktree path (got %s)", row.WorkingDir)
}

func TestOpenAgent_CreateBranch_EndToEnd(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return map[string]string{}, nil
	}

	repoDir := initRepo(t)
	branch := "feature/e2e-branch"
	dispatch(d, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{
		WorkingDir:    repoDir,
		GitSetup:      createBranchSetup(branch, ""),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	require.Eventually(t, func() bool {
		return localBranchExists(t, repoDir, branch)
	}, 5*time.Second, 20*time.Millisecond, "expected branch to be created")
}

// ---------- validateGitMode: fan-out gate ordering ----------
//
// validateCreateWorktree / validateCreateBranch fan out their rev-parse
// probes via errgroup, then gate the captured booleans in the original
// serial order. These tests lock that order in so a future refactor can't
// flip "branch already exists" to "base branch does not exist" or vice
// versa.

func TestValidateGitMode_CreateWorktreeBranchExistsTakesPrecedenceOverMissingBase(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/taken")
	run(t, repoDir, "git", "checkout", "-")
	svc, _, _ := setupTestService(t)

	_, err := svc.validateGitMode(context.Background(), repoDir, createWorktreeSetup("feature/taken", "does-not-exist"))
	require.Error(t, err)
	// Both branchInLocal and !(baseExists||baseIsRemote) hold; the gate
	// must surface the branch-collision error first to match pre-fanout
	// behavior.
	assert.Contains(t, err.Error(), "already exists")
	assert.NotContains(t, err.Error(), "base branch")
}

func TestValidateGitMode_CreateBranchExistsTakesPrecedenceOverMissingBase(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/taken")
	run(t, repoDir, "git", "checkout", "-")
	svc, _, _ := setupTestService(t)

	_, err := svc.validateGitMode(context.Background(), repoDir, createBranchSetup("feature/taken", "does-not-exist"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.NotContains(t, err.Error(), "base branch")
}

// validateCreateBranch and validateCheckoutBranch now run queryGitPathInfo
// in parallel with their LookupRef probes; the "not a git repository"
// gate must still fire before any branch-existence gate when the path is
// not a repo, so the user sees the most informative error.
func TestValidateGitMode_CreateBranchNonGitDirSurfacesRepoError(t *testing.T) {
	t.Parallel()

	notRepo := t.TempDir()
	svc, _, _ := setupTestService(t)

	_, err := svc.validateGitMode(context.Background(), notRepo, createBranchSetup("feature/anything", ""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not inside a git repository")
	assert.NotContains(t, err.Error(), "already exists")
	assert.NotContains(t, err.Error(), "base branch")
}

func TestValidateGitMode_CheckoutBranchNonGitDirSurfacesRepoError(t *testing.T) {
	t.Parallel()

	notRepo := t.TempDir()
	svc, _, _ := setupTestService(t)

	_, err := svc.validateGitMode(context.Background(), notRepo, checkoutBranchSetup("main"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not inside a git repository")
	assert.NotContains(t, err.Error(), "does not exist")
}

func TestValidateGitMode_CheckoutBranchMissingBranchInRepo(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, _, _ := setupTestService(t)

	_, err := svc.validateGitMode(context.Background(), repoDir, checkoutBranchSetup("nope/does-not-exist"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"nope/does-not-exist"`)
	assert.Contains(t, err.Error(), "does not exist")
}

// TestValidateGitMode_CreateBranchCancelledCtxSurfacesCancellation pins
// the regression where probeIsRepo and probeRef swallow their own
// errors, and an earlier revision then inferred "not a git repository"
// from the resulting zero-valued isRepo. That made a user-cancelled
// click look like a misleading repo error in the UI. The validator now
// surfaces ctx.Err() ahead of the probe-derived signals.
func TestValidateGitMode_CreateBranchCancelledCtxSurfacesCancellation(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, _, _ := setupTestService(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the call

	_, err := svc.validateGitMode(ctx, repoDir, createBranchSetup("newbranch", "main"))
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled,
		"a cancelled ctx must surface as context.Canceled, not a misleading 'not inside a git repository'")
	assert.NotContains(t, err.Error(), "is not inside a git repository")
}

// TestValidateGitMode_CheckoutBranchCancelledCtxSurfacesCancellation
// mirrors the above for the checkout-branch validator.
func TestValidateGitMode_CheckoutBranchCancelledCtxSurfacesCancellation(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, _, _ := setupTestService(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := svc.validateGitMode(ctx, repoDir, checkoutBranchSetup("main"))
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, err.Error(), "is not inside a git repository")
}

// TestValidateGitModeMapsEachSetupCase gives every shape of GitSetup one
// defined result. An absent setup uses the directory as it is. A present setup
// that selects no mode, or a mode whose required value is empty, is refused
// before any git command runs, so the refusals also hold in a directory that
// is not a repository. A valid mode reaches its per-mode validator.
func TestValidateGitModeMapsEachSetupCase(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	notRepo := t.TempDir()
	svc, _, _ := setupTestService(t)

	tests := []struct {
		name    string
		dir     string
		setup   *leapmuxv1.GitSetup
		mode    gitModeKind
		wantErr string
	}{
		{name: "absent", dir: repoDir, setup: nil, mode: gitModeUseCurrent},
		{name: "present with no mode", dir: notRepo, setup: &leapmuxv1.GitSetup{}, wantErr: "git_setup selects no mode"},
		{name: "create_worktree with an empty branch", dir: notRepo, setup: createWorktreeSetup("", "main"), wantErr: "create_worktree.branch is required"},
		{name: "create_worktree with a nil message", dir: notRepo, setup: &leapmuxv1.GitSetup{Mode: &leapmuxv1.GitSetup_CreateWorktree{}}, wantErr: "create_worktree.branch is required"},
		{name: "create_worktree", dir: repoDir, setup: createWorktreeSetup("feature/new", ""), mode: gitModeCreateWorktree},
		{name: "empty worktree path", dir: notRepo, setup: useWorktreePathSetup(""), wantErr: "invalid worktree path: path is empty"},
		{name: "checkout_branch empty", dir: notRepo, setup: checkoutBranchSetup(""), wantErr: "checkout_branch is required"},
		{name: "create_branch with an empty name", dir: notRepo, setup: createBranchSetup("", "main"), wantErr: "create_branch.name is required"},
		{name: "create_branch with a nil message", dir: notRepo, setup: &leapmuxv1.GitSetup{Mode: &leapmuxv1.GitSetup_CreateBranch{}}, wantErr: "create_branch.name is required"},
		{name: "create_branch", dir: repoDir, setup: createBranchSetup("feature/fresh", ""), mode: gitModeCreateBranch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			plan, err := svc.validateGitMode(context.Background(), tt.dir, tt.setup)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.mode, plan.Mode)
		})
	}
}

// OpenAgent refuses a present git setup that selects no mode before it
// creates a DB row, so the refusal reaches the caller as InvalidArgument.
func TestOpenAgentRefusesAGitSetupWithNoMode(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return map[string]string{}, nil
	}
	repoDir := initRepo(t)

	dispatch(d, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{
		WorkingDir:    repoDir,
		GitSetup:      &leapmuxv1.GitSetup{},
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)

	requireInvalidArgument(t, w)
	assert.Zero(t, countAgentRows(t, svc), "no DB row for a refused request")
}
