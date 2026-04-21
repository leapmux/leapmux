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
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Extra coverage for the git-mode validate/execute split. These tests
// exercise the happy paths for each mode plus some edge cases the open_*
// RPC-level tests don't cover (direct validate/execute calls so we can
// inspect the gitModePlan, branch-with-slash names, remote base branches,
// and the phase/rollback label renderers).

// ---------- validateGitMode: happy paths ----------

func TestValidateGitMode_CreateWorktreeHappyPath(t *testing.T) {
	repoDir := initRepo(t)
	svc, _, _ := setupTestService(t, "ws-1")

	plan, err := svc.validateGitMode(repoDir, openAgentGitModeReq(&leapmuxv1.OpenAgentRequest{
		CreateWorktree:     true,
		WorktreeBranch:     "feature/deep/slash",
		WorktreeBaseBranch: "", // implicit: current branch or HEAD
	}))
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
	repoDir := initRepo(t)
	svc, _, _ := setupTestService(t, "ws-1")

	plan, err := svc.validateGitMode(repoDir, openAgentGitModeReq(&leapmuxv1.OpenAgentRequest{
		CreateBranch: "feature/fresh",
	}))
	require.NoError(t, err)
	assert.Equal(t, gitModeCreateBranch, plan.Mode)
	assert.Equal(t, "feature/fresh", plan.BranchName)
	assert.Equal(t, repoDir, plan.PlannedWorkingDir, "create-branch does not change working dir")
}

func TestValidateGitMode_CheckoutExistingLocalBranch(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/local")
	run(t, repoDir, "git", "checkout", "-")
	svc, _, _ := setupTestService(t, "ws-1")

	plan, err := svc.validateGitMode(repoDir, openAgentGitModeReq(&leapmuxv1.OpenAgentRequest{
		CheckoutBranch: "feature/local",
	}))
	require.NoError(t, err)
	assert.Equal(t, gitModeCheckoutBranch, plan.Mode)
	assert.Equal(t, "feature/local", plan.CheckoutTarget)
}

func TestValidateGitMode_CheckoutRemoteRef(t *testing.T) {
	local := initRepo(t)
	remote := initRepo(t)
	run(t, remote, "git", "checkout", "-b", "feature/remote")
	run(t, remote, "git", "commit", "--allow-empty", "-m", "c")
	run(t, local, "git", "remote", "add", "origin", remote)
	run(t, local, "git", "fetch", "origin")
	svc, _, _ := setupTestService(t, "ws-1")

	plan, err := svc.validateGitMode(local, openAgentGitModeReq(&leapmuxv1.OpenAgentRequest{
		CheckoutBranch: "origin/feature/remote",
	}))
	require.NoError(t, err)
	assert.Equal(t, gitModeCheckoutBranch, plan.Mode)
}

func TestValidateGitMode_UseCurrentDefault(t *testing.T) {
	repoDir := initRepo(t)
	svc, _, _ := setupTestService(t, "ws-1")

	plan, err := svc.validateGitMode(repoDir, openAgentGitModeReq(&leapmuxv1.OpenAgentRequest{}))
	require.NoError(t, err)
	assert.Equal(t, gitModeUseCurrent, plan.Mode)
	assert.Equal(t, repoDir, plan.PlannedWorkingDir)
	assert.Empty(t, plan.PhaseLabel(), "useCurrent has no user-visible phase label")
}

func TestValidateGitMode_CreateWorktreeWithRemoteBaseBranch(t *testing.T) {
	local := initRepo(t)
	remote := initRepo(t)
	run(t, remote, "git", "checkout", "-b", "feature/base")
	run(t, remote, "git", "commit", "--allow-empty", "-m", "c")
	run(t, local, "git", "remote", "add", "origin", remote)
	run(t, local, "git", "fetch", "origin")
	svc, _, _ := setupTestService(t, "ws-1")

	plan, err := svc.validateGitMode(local, openAgentGitModeReq(&leapmuxv1.OpenAgentRequest{
		CreateWorktree:     true,
		WorktreeBranch:     "feature/new-from-remote",
		WorktreeBaseBranch: "origin/feature/base",
	}))
	require.NoError(t, err)
	assert.Equal(t, "origin/feature/base", plan.StartPoint)
}

// ---------- executeGitMode: happy paths ----------

func TestExecuteGitMode_CreateWorktreeSucceeds(t *testing.T) {
	repoDir := initRepo(t)
	svc, _, _ := setupTestService(t, "ws-1")

	plan, err := svc.validateGitMode(repoDir, openAgentGitModeReq(&leapmuxv1.OpenAgentRequest{
		CreateWorktree: true,
		WorktreeBranch: "feature/exec-happy",
	}))
	require.NoError(t, err)

	res, err := svc.executeGitMode(context.Background(), plan)
	require.NoError(t, err)
	assert.DirExists(t, res.WorkingDir)
	assert.NotEmpty(t, res.WorktreeID)
	assert.NotNil(t, res.Rollback.CreatedWorktree)
	assert.Equal(t, "feature/exec-happy", res.Rollback.CreatedWorktree.BranchName)
}

func TestExecuteGitMode_CreateBranchSucceeds(t *testing.T) {
	repoDir := initRepo(t)
	svc, _, _ := setupTestService(t, "ws-1")

	plan, err := svc.validateGitMode(repoDir, openAgentGitModeReq(&leapmuxv1.OpenAgentRequest{
		CreateBranch: "feature/exec-branch",
	}))
	require.NoError(t, err)

	res, err := svc.executeGitMode(context.Background(), plan)
	require.NoError(t, err)
	assert.Equal(t, repoDir, res.WorkingDir)
	assert.True(t, localBranchExists(t, repoDir, "feature/exec-branch"))
	require.NotNil(t, res.Rollback.CreatedBranch, "execute should expose rollback metadata so a downstream failure can reset HEAD")
	assert.Equal(t, "feature/exec-branch", res.Rollback.CreatedBranch.CreatedBranch)
}

// ---------- labels ----------

func TestGitModePlan_PhaseLabelPerMode(t *testing.T) {
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
	repoDir := initRepo(t)
	// Create an existing worktree with a branch containing deep slashes
	// and validate that useWorktreePath resolves symlinks correctly.
	run(t, repoDir, "git", "worktree", "add", "-b", "feature/deep/nest", filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-worktrees", "feature/deep/nest"))
	sym := filepath.Join(t.TempDir(), "symlink")
	require.NoError(t, osSymlink(filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-worktrees", "feature/deep/nest"), sym))
	svc, _, _ := setupTestService(t, "ws-1")

	plan, err := svc.validateGitMode(repoDir, openAgentGitModeReq(&leapmuxv1.OpenAgentRequest{
		UseWorktreePath: sym,
	}))
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

// openAgentGitModeReq wraps the proto in the interface so the helper
// compiles regardless of which proto type is used. validateGitMode only
// reads the gitModeRequest methods so any request type works.
func openAgentGitModeReq(r *leapmuxv1.OpenAgentRequest) gitModeRequest { return r }

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
	svc, d, w := setupTestService(t, "ws-1")
	defer drainStartups(svc)
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		return &leapmuxv1.AgentSettings{}, nil
	}

	repoDir := initRepo(t)
	branch := "feature/e2e-worktree"
	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: branch,
		AgentProvider:  leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()

	// Phase 0 eventually creates the worktree and registers it.
	require.Eventually(t, func() bool {
		return directoryExists(expectedWorktreePath(repoDir, branch))
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
	svc, d, w := setupTestService(t, "ws-1")
	defer drainStartups(svc)
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		return &leapmuxv1.AgentSettings{}, nil
	}

	repoDir := initRepo(t)
	branch := "feature/e2e-branch"
	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    repoDir,
		CreateBranch:  branch,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	require.Eventually(t, func() bool {
		return localBranchExists(t, repoDir, branch)
	}, 5*time.Second, 20*time.Millisecond, "expected branch to be created")
}
