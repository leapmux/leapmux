package service

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func branchWorktreeSelector(branch string) *leapmuxv1.GitWorktreeSelector {
	return &leapmuxv1.GitWorktreeSelector{Selector: &leapmuxv1.GitWorktreeSelector_Branch{Branch: branch}}
}

func TestResolveGitWorktreeMatchesAnExactAttachedBranchInOneRepo(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo, otherRepo := initRepo(t), initRepo(t)
	parent := t.TempDir()
	const branch = "origin/Feature"
	path, otherPath := filepath.Join(parent, "checkout"), filepath.Join(parent, "other")
	run(t, repo, "git", "worktree", "add", "-b", branch, path)
	run(t, otherRepo, "git", "worktree", "add", "-b", branch, otherPath)
	run(t, repo, "git", "branch", "unplaced")
	run(t, repo, "git", "worktree", "lock", "--reason", "reserve the checkout", path)
	info, entry, err := svc.resolveGitWorktree(t.Context(), repo, branchWorktreeSelector(branch))
	require.NoError(t, err)
	assert.Equal(t, pathutil.Canonicalize(repo), info.RepoRoot)
	assert.Equal(t, pathutil.Canonicalize(path), entry.Path)
	assert.Equal(t, branch, entry.Branch)
	assert.False(t, entry.IsMain)
	assert.True(t, entry.Locked)
	assert.Equal(t, "reserve the checkout", entry.LockReason)
	_, qualified, err := svc.resolveGitWorktree(t.Context(), repo, branchWorktreeSelector("refs/heads/"+branch))
	require.NoError(t, err)
	assert.Equal(t, entry, qualified)
	for _, branch := range []string{"origin/feature", "missing", "unplaced"} {
		_, _, err := svc.resolveGitWorktree(t.Context(), repo, branchWorktreeSelector(branch))
		assert.ErrorIs(t, err, errGitWorktreeNotFound)
	}
	_, _, err = svc.resolveGitWorktree(t.Context(), otherRepo, useWorktreePathSetup(path).GetUseWorktree())
	assert.ErrorIs(t, err, errGitWorktreeNotFound)
}

func TestResolveGitWorktreeRefusesMultipleCheckoutsOfABranch(t *testing.T) {
	t.Parallel()
	svc, dispatcher, writer := setupTestService(t)
	repo := initRepo(t)
	parent := t.TempDir()
	pathA, pathZ := filepath.Join(parent, "a"), filepath.Join(parent, "z")
	run(t, repo, "git", "worktree", "add", "-b", "duplicate", pathZ)
	run(t, repo, "git", "worktree", "add", "--force", pathA, "duplicate")
	_, _, err := svc.resolveGitWorktree(t.Context(), repo, branchWorktreeSelector("duplicate"))
	require.ErrorIs(t, err, errGitWorktreeAmbiguous)
	assert.Contains(t, err.Error(), strconv.Quote("path:"+pathutil.Canonicalize(pathA))+", "+strconv.Quote("path:"+pathutil.Canonicalize(pathZ)))
	dispatch(dispatcher, leapmuxv1connect.WorkerGitServiceGetGitWorktreeProcedure, &leapmuxv1.GetGitWorktreeRequest{
		RepoDir: repo, Target: &leapmuxv1.GetGitWorktreeRequest_Selector{Selector: branchWorktreeSelector("duplicate")},
	}, writer)
	requireOK(t, writer)
	var response leapmuxv1.GetGitWorktreeResponse
	require.NoError(t, proto.Unmarshal(writer.responses[0].GetPayload(), &response))
	assert.Nil(t, response.GetWorktree())
	assert.Equal(t, []string{pathutil.Canonicalize(pathA), pathutil.Canonicalize(pathZ)}, response.GetAmbiguousPaths())
}

func TestResolveGitWorktreeReusesTheReportedBareRepository(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	bare, checkout := newBareCheckout(t, "bare-worktrees.git")
	info, entry, err := svc.resolveGitWorktree(t.Context(), "", useWorktreePathSetup(checkout).GetUseWorktree())
	require.NoError(t, err)
	require.Equal(t, bare, info.RepoRoot)
	assert.Equal(t, checkout, entry.Path)
	_, byBranch, err := svc.resolveGitWorktree(t.Context(), info.RepoRoot, branchWorktreeSelector("refs/heads/main"))
	require.NoError(t, err)
	assert.Equal(t, entry, byBranch)
}

func TestWorktreeBranchSetupPreservesTheCheckoutAndItsAttachedRef(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "checkout")
	run(t, repo, "git", "worktree", "add", "-b", "selected", path)
	selector := branchWorktreeSelector("refs/heads/selected")
	setup := &leapmuxv1.GitSetup{Mode: &leapmuxv1.GitSetup_UseWorktree{UseWorktree: selector}}
	_, err := svc.openGitWorkingDir(repo, setup, channel.Caller{})
	require.ErrorIs(t, err, errGitWorktreeReadScope)
	plan, err := svc.validateGitMode(t.Context(), repo, setup)
	require.NoError(t, err)
	require.Equal(t, "selected", plan.SelectedWorktree.RequiredBranch)
	unchanged, err := svc.executeGitMode(t.Context(), plan)
	require.NoError(t, err)
	require.Equal(t, pathutil.Canonicalize(path), unchanged.WorkingDir)

	run(t, path, "git", "switch", "-c", "replacement")
	_, err = svc.executeGitMode(t.Context(), plan)
	require.ErrorIs(t, err, errSelectedWorktreeChanged)
	byPath, err := svc.validateGitMode(t.Context(), repo, useWorktreePathSetup(path))
	require.NoError(t, err)
	assert.Empty(t, byPath.SelectedWorktree.RequiredBranch)
	assert.Equal(t, plan.SelectedWorktree.CheckoutID, byPath.SelectedWorktree.CheckoutID)
}

func TestGetGitWorktreeSelfUsesTheAuthenticatedTabDirectory(t *testing.T) {
	t.Parallel()
	svc, dispatcher, writer := setupTestService(t)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "detached")
	run(t, repo, "git", "worktree", "add", "--detach", path)
	child := filepath.Join(path, "project", "subdir")
	require.NoError(t, os.MkdirAll(child, 0o755))
	require.NoError(t, svc.Queries.UpsertTerminal(t.Context(), db.UpsertTerminalParams{
		ID: "worktree-self", WorkingDir: child, Cols: 80, Rows: 24, Screen: []byte{},
	}))
	request := &leapmuxv1.GetGitWorktreeRequest{Target: &leapmuxv1.GetGitWorktreeRequest_Self{Self: true}}
	dispatchFromTab(dispatcher, "worktree-self", leapmuxv1connect.WorkerGitServiceGetGitWorktreeProcedure, request, writer)
	requireOK(t, writer)
	var response leapmuxv1.GetGitWorktreeResponse
	require.NoError(t, proto.Unmarshal(writer.responses[0].GetPayload(), &response))
	assert.Equal(t, pathutil.Canonicalize(path), response.GetWorktree().GetPath())
	assert.Empty(t, response.GetWorktree().GetBranch())
	assert.False(t, response.GetWorktree().GetIsMain())
	assert.Equal(t, pathutil.Canonicalize(repo), response.GetRepoRoot())
	request.RepoDir = repo
	conflict := newTestWriter()
	dispatchFromTab(dispatcher, "worktree-self", leapmuxv1connect.WorkerGitServiceGetGitWorktreeProcedure, request, conflict)
	assert.Contains(t, requireInvalidArgument(t, conflict), "calling tab")
	request.RepoDir = ""
	noTab := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerGitServiceGetGitWorktreeProcedure, request, noTab)
	assert.Contains(t, requireInvalidArgument(t, noTab), "calling tab")
	relayed := channel.LocalAgentCaller(userid.MustNew("user-1"))
	relayed.SourceTabID = "worktree-self"
	relayed.RelayWorkerID = "other-worker"
	payload, err := proto.Marshal(request)
	require.NoError(t, err)
	remote := newTestWriter()
	dispatchTestRequest(dispatcher, t.Context(), relayed, &leapmuxv1.InnerRpcRequest{
		Method: leapmuxv1connect.WorkerGitServiceGetGitWorktreeProcedure, Payload: payload,
	}, remote)
	assert.Contains(t, requireInvalidArgument(t, remote), "calling tab")
}

func TestGetGitWorktreeSelfReportsNoCheckoutOutsideGit(t *testing.T) {
	t.Parallel()
	svc, dispatcher, writer := setupTestService(t)
	require.NoError(t, svc.Queries.UpsertTerminal(t.Context(), db.UpsertTerminalParams{
		ID: "outside-git", WorkingDir: t.TempDir(), Cols: 80, Rows: 24, Screen: []byte{},
	}))
	dispatchFromTab(dispatcher, "outside-git", leapmuxv1connect.WorkerGitServiceGetGitWorktreeProcedure,
		&leapmuxv1.GetGitWorktreeRequest{Target: &leapmuxv1.GetGitWorktreeRequest_Self{Self: true}}, writer)
	require.Empty(t, writer.responses)
	require.Len(t, writer.errors, 1)
	assert.Equal(t, int32(codes.NotFound), writer.errors[0].code)
}

func TestOpenTerminalByWorktreeBranchRequiresGitRead(t *testing.T) {
	t.Parallel()
	svc, dispatcher, writer := setupTestService(t)
	scopes := authscope.MustNew(leapmuxv1.Scope_SCOPE_TERMINAL_WRITE, leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE, leapmuxv1.Scope_SCOPE_WORKER_READ)
	dispatchScoped(dispatcher, scopes, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
		Target:     &leapmuxv1.OpenTerminalRequest_Creation{Creation: creationIntent("worktree-no-git-read")},
		WorkingDir: initRepo(t),
		GitSetup:   &leapmuxv1.GitSetup{Mode: &leapmuxv1.GitSetup_UseWorktree{UseWorktree: branchWorktreeSelector("selected")}},
	}, writer)
	require.Empty(t, writer.responses)
	require.Len(t, writer.errors, 1)
	assert.Equal(t, int32(codes.PermissionDenied), writer.errors[0].code)
	assert.Contains(t, writer.errors[0].message, "branch selection requires git:read")
	assert.Zero(t, countTerminalRows(t, svc))
}

func TestOpenAgentByWorktreeBranchRequiresGitRead(t *testing.T) {
	t.Parallel()
	svc, dispatcher, writer := setupTestService(t)
	scopes := authscope.MustNew(leapmuxv1.Scope_SCOPE_AGENT_WRITE, leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE, leapmuxv1.Scope_SCOPE_WORKER_READ)
	dispatchScoped(dispatcher, scopes, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{
		Creation:      creationIntent("agent-no-git-read"),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		WorkingDir:    initRepo(t),
		GitSetup:      &leapmuxv1.GitSetup{Mode: &leapmuxv1.GitSetup_UseWorktree{UseWorktree: branchWorktreeSelector("selected")}},
	}, writer)
	require.Empty(t, writer.responses)
	require.Len(t, writer.errors, 1)
	assert.Equal(t, int32(codes.PermissionDenied), writer.errors[0].code)
	assert.Contains(t, writer.errors[0].message, "branch selection requires git:read")
	assert.Zero(t, countAgentRows(t, svc))
}

func TestTerminalLaunchRechecksTheSelectedWorktreeBranch(t *testing.T) {
	t.Parallel()
	for _, beforeFirstLaunch := range []bool{true, false} {
		name := "restart"
		if beforeFirstLaunch {
			name = "first launch"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			clock := testutil.NewQuartzMock(t)
			resizeStart, resizeStop := testutil.NewTimerTraps(t, clock, startupPendingResizeTimerTag)
			svc, dispatcher, writer := setupTestService(t, withClock(clock))
			spawned := make(chan string, 1)
			svc.startTerminalFn = func(_ context.Context, opts terminal.Options, _ terminal.OutputHandler, _ terminal.ExitHandler) error {
				spawned <- opts.WorkingDir
				return nil
			}
			repo := initRepo(t)
			path := filepath.Join(t.TempDir(), "checkout")
			run(t, repo, "git", "worktree", "add", "-b", "wanted", path)
			dispatch(dispatcher, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
				WorkingDir: repo, Shell: testutil.TestShell(), Cols: 80, Rows: 24,
				GitSetup: &leapmuxv1.GitSetup{Mode: &leapmuxv1.GitSetup_UseWorktree{UseWorktree: branchWorktreeSelector("refs/heads/wanted")}},
			}, writer)
			requireOK(t, writer)
			var opened leapmuxv1.OpenTerminalResponse
			require.NoError(t, proto.Unmarshal(writer.responses[0].GetPayload(), &opened))
			id := opened.GetTerminalId()
			testutil.RegisterTerminalCleanup(t, svc.Terminals, id)
			delay := testutil.WaitForTimer(t, ctx, resizeStart)
			if beforeFirstLaunch {
				run(t, path, "git", "switch", "-c", "replacement")
			}
			testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, resizeStop)
			svc.TerminalStartup.WaitForInFlight()
			if !beforeFirstLaunch {
				require.Len(t, spawned, 1)
				assert.Equal(t, pathutil.Canonicalize(path), <-spawned)
				restarted := newTestWriter()
				dispatchRestart(dispatcher, id, restarted)
				requireOK(t, restarted)
				svc.TerminalStartup.WaitForInFlight()
				require.True(t, svc.Terminals.IsRunning(id))
				svc.Terminals.StopTerminal(id)
				require.NoError(t, svc.Terminals.WaitForExitContext(ctx, id))
				run(t, path, "git", "switch", "-c", "replacement")
				changed := newTestWriter()
				dispatchRestart(dispatcher, id, changed)
				requireOK(t, changed)
				svc.TerminalStartup.WaitForInFlight()
			}
			require.Empty(t, spawned)
			assert.False(t, svc.Terminals.IsRunning(id))
			row, err := svc.Queries.GetTerminal(ctx, id)
			require.NoError(t, err)
			assert.Contains(t, row.StartupError, `branch "wanted" is no longer checked out there`)
		})
	}
}
