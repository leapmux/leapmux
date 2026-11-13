package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestBareRepositoryLifecycleKeepsCommonContext(t *testing.T) {
	bare, checkout := newBareCheckout(t, "project.git")
	svc, _, _ := setupTestService(t)
	info, err := queryGitPathInfo(t.Context(), checkout)
	require.NoError(t, err)
	assert.Equal(t, bare, info.CommonDir)
	assert.Empty(t, info.MainWorktreeRoot)
	branches, err := listGitBranches(t.Context(), checkout)
	require.NoError(t, err)
	require.NotEmpty(t, branches)
	plan, err := svc.validateCreateWorktree(t.Context(), checkout, "created", "")
	require.NoError(t, err)
	assert.Equal(t, bare, plan.RepoRoot)
	assert.Equal(t, bare, plan.RepoGitDir)
	result, err := svc.executeGitMode(t.Context(), plan)
	require.NoError(t, err)
	tracked, err := svc.Queries.GetWorktreeByID(t.Context(), result.WorktreeID)
	require.NoError(t, err)
	assert.Equal(t, bare, tracked.RepoGitDir)
	second, err := queryGitPathInfo(t.Context(), result.WorkingDir)
	require.NoError(t, err)
	assert.Equal(t, info.RepoRoot, second.RepoRoot)
	assert.Same(t, svc.gitIndexLock(info.CommonDir), svc.gitIndexLock(second.CommonDir))
	intent, err := svc.prepareCreationStartup(t.Context(), plan)
	require.NoError(t, err)
	intent.Result = &result
	snapshot := struct {
		Intent   creationStartupIntent
		Snapshot tabCloseSnapshot
		Removal  worktreeRemovalCheckpoint
	}{
		Intent: intent, Snapshot: tabCloseSnapshot{Worktree: &tracked, RemovalInfo: second},
		Removal: worktreeRemovalCheckpoint{WorktreeID: tracked.ID, WorktreePath: tracked.WorktreePath, RepoRoot: tracked.RepoRoot, RepoGitDir: tracked.RepoGitDir},
	}
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	var restored struct {
		Intent   creationStartupIntent
		Snapshot tabCloseSnapshot
		Removal  worktreeRemovalCheckpoint
	}
	require.NoError(t, json.Unmarshal(encoded, &restored))
	assert.Equal(t, bare, restored.Intent.Plan.RepoGitDir)
	assert.Equal(t, bare, restored.Intent.Result.RepoGitDir)
	assert.Equal(t, bare, restored.Intent.Result.Rollback.CreatedWorktree.RepoGitDir)
	assert.Equal(t, bare, restored.Snapshot.Worktree.RepoGitDir)
	assert.Equal(t, bare, restored.Removal.RepoGitDir)
	require.NoError(t, svc.removeWorktreeFromDisk(tracked))
	_, err = os.Stat(result.WorkingDir)
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = (gitutil.Repository{GitDir: bare}).Output(t.Context(), "show-ref", "--verify", "refs/heads/created")
	assert.Error(t, err)
	// The stored context remains usable after a manually deleted checkout.
	plan, err = svc.validateCreateWorktree(t.Context(), checkout, "missing", "")
	require.NoError(t, err)
	result, err = svc.executeGitMode(t.Context(), plan)
	require.NoError(t, err)
	tracked, err = svc.Queries.GetWorktreeByID(t.Context(), result.WorktreeID)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(result.WorkingDir))
	repository, target, err := svc.worktreeListingFor(t.Context(), result.WorkingDir, nil)
	require.NoError(t, err)
	assert.Equal(t, bare, repository.GitDir)
	assert.Equal(t, tracked.WorktreePath, target)
	entries, err := repository.Worktrees(t.Context())
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(entries), 2)
	require.NoError(t, repository.Run(t.Context(), "worktree", "prune", "--expire", "now"))
	require.NoError(t, svc.removeWorktreeFromDisk(tracked))
	// A missing checkout cannot prove which live branch it held. Keep that ref.
	_, err = repository.Output(t.Context(), "show-ref", "--verify", "refs/heads/missing")
	require.NoError(t, err)
}

func TestGitInfoSeparatesAttachedAndDisplay(t *testing.T) {
	repo := initRepo(t)
	a := strings.TrimSpace(run(t, repo, "git", "rev-parse", "HEAD"))
	label := a[:7]
	run(t, repo, "git", "commit", "--allow-empty", "-m", "B")
	b := strings.TrimSpace(run(t, repo, "git", "rev-parse", "HEAD"))
	run(t, repo, "git", "branch", label)
	run(t, repo, "git", "checkout", "--detach", a)
	svc, dispatcher, writer := setupTestService(t)
	dispatch(dispatcher, leapmuxv1connect.WorkerGitServiceGetGitInfoProcedure, &leapmuxv1.GetGitInfoRequest{Path: repo}, writer)
	require.Empty(t, writer.errors)
	require.Len(t, writer.responses, 1)
	var response leapmuxv1.GetGitInfoResponse
	require.NoError(t, proto.Unmarshal(writer.responses[0].GetPayload(), &response))
	assert.Equal(t, label, response.CurrentBranch)
	assert.Empty(t, response.AttachedBranch)
	dispatch(dispatcher, leapmuxv1connect.WorkerGitServiceListGitBranchesProcedure, &leapmuxv1.ListGitBranchesRequest{Path: repo}, writer)
	require.Empty(t, writer.errors)
	require.Len(t, writer.responses, 2)
	var listed leapmuxv1.ListGitBranchesResponse
	require.NoError(t, proto.Unmarshal(writer.responses[1].GetPayload(), &listed))
	assert.Equal(t, label, listed.CurrentBranch)
	assert.Empty(t, listed.AttachedBranch)
	assert.Equal(t, pathutil.Canonicalize(repo), response.MainWorktreeRoot)
	change, err := svc.inspectBranchChange(t.Context(), repo)
	require.NoError(t, err)
	assert.Empty(t, change.AttachedBranch)
	assert.Equal(t, label, change.CurrentBranch)
	for _, base := range []struct{ value, want string }{{"", a}, {label, b}} {
		name := "from-head"
		if base.value != "" {
			name = "from-explicit"
		}
		require.NoError(t, createBranchInDir(t.Context(), repo, name, base.value))
		assert.Equal(t, base.want, strings.TrimSpace(run(t, repo, "git", "rev-parse", "HEAD")))
		run(t, repo, "git", "checkout", "--detach", a)
	}
	for _, base := range []struct{ value, want string }{{"", a}, {label, b}} {
		name := "worktree-head"
		if base.value != "" {
			name = "worktree-explicit"
		}
		plan, err := svc.validateCreateWorktree(t.Context(), repo, name, base.value)
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(plan.WorktreePath)) })
		result, err := svc.executeGitMode(t.Context(), plan)
		require.NoError(t, err)
		assert.Equal(t, base.want, strings.TrimSpace(run(t, result.WorkingDir, "git", "rev-parse", "HEAD")))
	}
	run(t, repo, "git", "checkout", label)
	info, err := queryGitPathInfo(t.Context(), repo)
	require.NoError(t, err)
	assert.Equal(t, label, info.BranchName)
	before := len(writer.responses)
	dispatch(dispatcher, leapmuxv1connect.WorkerGitServiceGetGitInfoProcedure, &leapmuxv1.GetGitInfoRequest{Path: repo}, writer)
	require.Empty(t, writer.errors)
	require.Len(t, writer.responses, before+1)
	require.NoError(t, proto.Unmarshal(writer.responses[len(writer.responses)-1].GetPayload(), &response))
	assert.Equal(t, label, response.AttachedBranch)
	dispatch(dispatcher, leapmuxv1connect.WorkerGitServiceListGitBranchesProcedure, &leapmuxv1.ListGitBranchesRequest{Path: repo}, writer)
	require.Empty(t, writer.errors)
	require.Len(t, writer.responses, before+2)
	require.NoError(t, proto.Unmarshal(writer.responses[len(writer.responses)-1].GetPayload(), &listed))
	assert.Equal(t, label, listed.AttachedBranch)
	attached, err := svc.inspectBranchChange(t.Context(), repo)
	require.NoError(t, err)
	assert.Equal(t, label, attached.AttachedBranch)
	unborn := testutil.NewEmptyGitRepo(t)
	info, err = queryGitPathInfo(t.Context(), unborn)
	require.NoError(t, err)
	assert.Equal(t, "main", info.BranchName)
	assert.Empty(t, info.HeadSHA)
}

func TestGitAbsenceRejectsCleanupAndUnknownDiagnostics(t *testing.T) {
	_, _, failure := gitutil.Capture(t.Context(), t.TempDir(), "rev-parse", "--show-toplevel")
	var command *gitutil.CommandError
	require.ErrorAs(t, failure, &command)
	var native *exec.ExitError
	require.ErrorAs(t, failure, &native)
	valid := []byte("fatal: not a git repository (or any of the parent directories): .git\n")
	for _, test := range []struct {
		diagnostic string
		absent     bool
	}{
		{string(valid), true}, {"fatal: this operation must be run in a work tree\n", true},
		{"fatal: not a git repository (or any parent up to mount point /)\nStopping at filesystem boundary (GIT_DISCOVERY_ACROSS_FILESYSTEM not set).\n", true},
		{"fatal: not a git repository: /broken/pointer\n", false}, {"fatal: bad config line 1 in file config\n", false},
		{"unknown future Git diagnostic\n", false},
	} {
		native.Stderr = []byte(test.diagnostic)
		assert.Equal(t, test.absent, isGitWorktreeAbsent(failure), test.diagnostic)
	}
	native.Stderr = valid
	cleanupFailure := errors.New("owner cleanup failed")
	joined := &gitutil.CommandError{RunError: command.RunError, CleanupError: cleanupFailure}
	assert.False(t, isGitWorktreeAbsent(joined))
	assert.ErrorIs(t, joined, cleanupFailure)
}

func TestConfigReadPreservesOperationalFailures(t *testing.T) {
	repo := initRepo(t)
	_, err := readGitConfigRegexp(t.Context(), repo, "[")
	require.Error(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = readGitConfigRegexp(ctx, repo, "^missing$")
	require.ErrorIs(t, err, context.Canceled)
	config, err := readGitConfigRegexp(t.Context(), repo, "^missing$")
	require.NoError(t, err)
	assert.Empty(t, config)
	t.Setenv("GIT_CONFIG_GLOBAL", t.TempDir())
	_, err = readGitConfigRegexp(t.Context(), repo, "^missing$")
	require.Error(t, err)
	assert.ErrorContains(t, err, "unable to access")
}

func TestMutationFormattingKeepsFailureCauses(t *testing.T) {
	cleanup := errors.New("cleanup failed")
	cause := errors.Join(context.Canceled, cleanup)
	err := wrapGitErr("check out branch", "native diagnostic\n", cause)
	assert.Equal(t, "failed to check out branch: native diagnostic", err.Error())
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, err, cleanup)
}

func TestSeparateAdminPlacementUsesCommonAnchor(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "primary")
	admin := filepath.Join(base, "admin-storage")
	require.NoError(t, os.Mkdir(primary, 0o700))
	run(t, primary, "git", "-c", "core.fsmonitor=false", "init", "--initial-branch=main", "--separate-git-dir="+admin)
	for key, value := range map[string]string{"core.fsmonitor": "false", "gc.auto": "0", "maintenance.auto": "false", "user.name": "Test", "user.email": "test@example.invalid"} {
		run(t, primary, "git", "--git-dir="+admin, "config", key, value)
	}
	run(t, primary, "git", "commit", "--allow-empty", "-m", "initial")
	linked := filepath.Join(base, "linked")
	run(t, primary, "git", "worktree", "add", "-b", "linked", linked)
	svc, dispatcher, writer := setupTestService(t)
	first, err := queryGitPathInfo(t.Context(), primary)
	require.NoError(t, err)
	second, err := queryGitPathInfo(t.Context(), linked)
	require.NoError(t, err)
	admin = pathutil.Canonicalize(admin)
	assert.Empty(t, first.MainWorktreeRoot)
	assert.Empty(t, second.MainWorktreeRoot)
	assert.Equal(t, admin, first.RepoRoot)
	assert.Equal(t, admin, second.RepoRoot)
	assert.Equal(t, admin, first.CommonDir)
	assert.Equal(t, admin, second.CommonDir)
	assert.Same(t, svc.gitIndexLock(first.CommonDir), svc.gitIndexLock(second.CommonDir))
	tab, err := svc.loadGitContextForDir(t.Context(), primary)
	require.NoError(t, err)
	assert.Equal(t, pathutil.Canonicalize(primary), tab.commitDir(), "checkout operations never use the administrative placement anchor")
	dispatch(dispatcher, leapmuxv1connect.WorkerGitServiceGetGitInfoProcedure, &leapmuxv1.GetGitInfoRequest{Path: primary}, writer)
	require.Empty(t, writer.errors)
	require.Len(t, writer.responses, 1)
	var response leapmuxv1.GetGitInfoResponse
	require.NoError(t, proto.Unmarshal(writer.responses[0].GetPayload(), &response))
	assert.True(t, response.IsRepoRoot)
	assert.Empty(t, response.MainWorktreeRoot)
	for index, selected := range []string{primary, linked} {
		name := fmt.Sprintf("created-%d", index)
		plan, err := svc.validateCreateWorktree(t.Context(), selected, name, "")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(pathutil.Canonicalize(base), "admin-storage-worktrees", name), plan.WorktreePath)
		result, err := svc.executeGitMode(t.Context(), plan)
		require.NoError(t, err)
		intent, err := svc.prepareCreationStartup(t.Context(), plan)
		require.NoError(t, err)
		changed := result
		changed.RepoRoot = filepath.Join(base, "different-placement-anchor")
		operation := &creationOperation{coordinator: &tabCreationCoordinator{svc: svc}}
		require.NoError(t, operation.validateSavedCheckpoint(t.Context(), &intent, changed), "placement is not repository authority")
		wrong := changed
		wrong.RepoGitDir = filepath.Join(base, "different-common-directory")
		require.Error(t, operation.validateSavedCheckpoint(t.Context(), &intent, wrong), "a result cannot replace the saved plan's repository authority")
		used, err := svc.executeUseWorktreePath(t.Context(), gitModePlan{WorktreePath: result.WorkingDir})
		require.NoError(t, err)
		assert.Equal(t, admin, used.RepoGitDir)
		require.NoError(t, operation.validateSavedCheckpoint(t.Context(), &creationStartupIntent{Plan: gitModePlan{Mode: gitModeUseWorktreePath}}, used))
	}
}

func regularRepositoryGitDirForTest(t *testing.T, repoRoot string) string {
	t.Helper()
	absolute, err := filepath.Abs(filepath.Join(repoRoot, ".git"))
	require.NoError(t, err)
	return pathutil.Canonicalize(absolute)
}
