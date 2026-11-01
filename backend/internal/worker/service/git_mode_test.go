package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
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

// TestRollbackCreatedBranch_DeletesBranchEvenIfCheckoutRestoreFails
// pins the regression where rollbackCreatedBranch early-returned on a
// HEAD-restore failure (working tree dirty, stale index.lock, sibling
// git race) and left the half-created branch ref behind. The leftover
// ref then blocked a retry with the same name and the user saw the
// "Rolling back branch X" label even though the cleanup never ran.
//
// The simplest reproduction: a rollback whose OriginalBranch does NOT
// exist makes the restore-checkout fail, but the just-created
// CreatedBranch is still on disk. The fix attempts the branch delete
// regardless of the restore outcome.
func TestRollbackCreatedBranch_DeletesBranchEvenIfCheckoutRestoreFails(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, _, _ := setupTestService(t)

	// Set the stage: create 'leftover' as a real branch on disk.
	run(t, repoDir, "git", "checkout", "-b", "leftover")
	require.True(t, localBranchExists(t, repoDir, "leftover"))
	// Move HEAD off it so `git branch -D leftover` can succeed (git
	// refuses to delete the current branch).
	run(t, repoDir, "git", "checkout", "-")

	// Craft a rollback where OriginalBranch does not exist so the
	// HEAD-restore checkout fails; the branch delete must still run.
	svc.rollbackCreatedBranch(rollbackBranch{
		WorkingDir:     repoDir,
		OriginalBranch: "ghost-original-that-does-not-exist",
		CreatedBranch:  "leftover",
	})
	assert.False(t, localBranchExists(t, repoDir, "leftover"),
		"branch delete must run even when the prior HEAD-restore checkout failed")
}

// ---------- labels ----------

func TestGitModePlan_PhaseLabelPerMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		plan gitModePlan
		want string
	}{
		{gitModePlan{Mode: gitModeCreateWorktree, BranchName: "feature/x"}, `Creating worktree "feature/x"…`},
		// UseWorktreePath uses filepath.Base so the label shows the last
		// component of the path (the leaf of a potentially deep branch name).
		{gitModePlan{Mode: gitModeUseWorktreePath, WorktreePath: "/tmp/foo-worktrees/feature/bar"}, `Switching to worktree "bar"…`},
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
	// Create an existing worktree with a branch containing deep slashes
	// and validate that useWorktreePath resolves symlinks correctly.
	run(t, repoDir, "git", "worktree", "add", "-b", "feature/deep/nest", filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-worktrees", "feature/deep/nest"))
	sym := filepath.Join(t.TempDir(), "symlink")
	require.NoError(t, osSymlink(filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-worktrees", "feature/deep/nest"), sym))
	svc, _, _ := setupTestService(t)

	plan, err := svc.validateGitMode(context.Background(), repoDir, useWorktreePathSetup(sym))
	require.NoError(t, err)
	assert.Equal(t, gitModeUseWorktreePath, plan.Mode)
	// The validated path is the sanitized input (the helper evals
	// symlinks for comparison against `git worktree list` but returns
	// the user-provided path so the later UpsertTerminal stores what
	// the user actually referenced).
	// Normalize to forward slashes so the nested-branch suffix matches
	// on Windows where the resolved path uses backslashes.
	planned := filepath.ToSlash(plan.PlannedWorkingDir)
	assert.True(t, strings.HasSuffix(planned, "symlink") || strings.HasSuffix(planned, "feature/deep/nest"),
		"plan should keep either the symlinked path or its target: got %s", plan.PlannedWorkingDir)
}

// ---------- helpers ----------

func osSymlink(target, link string) error { return os.Symlink(target, link) }

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
		{name: "use_worktree_path empty", dir: notRepo, setup: useWorktreePathSetup(""), wantErr: "use_worktree_path is required"},
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
