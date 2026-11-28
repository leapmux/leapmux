package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/util/pathutil"
)

func TestInspectTabsClose_DirtyWorktreeAllSiblingsClosingPrompts(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t)
	bareDir := filepath.Join(t.TempDir(), "origin.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	initBareRepo(t, bareDir)

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")

	wtDir := filepath.Join(t.TempDir(), "dirty-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "feature-bulk", wtDir)
	run(t, wtDir, "git", "commit", "--allow-empty", "-m", "unpushed")
	run(t, wtDir, "git", "push", "-u", "origin", "feature-bulk")
	run(t, wtDir, "git", "push", "origin", "--delete", "feature-bulk")
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "dirty.txt"), []byte("dirty\n"), 0o644))

	wtID, err := svc.ensureTrackedWorktree(context.Background(), wtDir)
	require.NoError(t, err)
	createAgentForPath(t, svc, "agent-wt-1", wtDir)
	createAgentForPath(t, svc, "agent-wt-2", wtDir)
	svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-wt-1")
	svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-wt-2")

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerGitServiceInspectTabsCloseProcedure, &leapmuxv1.InspectTabsCloseRequest{
		Tabs: []*leapmuxv1.TabIdent{
			agentTabIdent("agent-wt-1"),
			agentTabIdent("agent-wt-2"),
		},
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	var resp leapmuxv1.InspectTabsCloseResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetItems(), 2)

	expectedWtPath, err := filepath.EvalSymlinks(wtDir)
	require.NoError(t, err)

	for i, wantID := range []string{"agent-wt-1", "agent-wt-2"} {
		item := resp.GetItems()[i]
		assert.Equal(t, wantID, item.GetTab().GetTabId())
		assert.True(t, item.GetShouldPrompt(), "closing all siblings on a dirty worktree must prompt")
		assert.True(t, item.GetIsWorktree())
		assert.True(t, pathutil.SamePath(expectedWtPath, item.GetWorktreePath()))
		assert.Equal(t, "feature-bulk", item.GetBranchName())
		assert.Empty(t, item.GetErrorHint())
		gs := item.GetGitState()
		require.NotNil(t, gs)
		assert.True(t, gs.GetHasUncommittedChanges())
		assert.Equal(t, int32(1), gs.GetUnpushedCommitCount())
		assert.True(t, gs.GetRemoteBranchMissing(), "deleted remote tracking branch must report RemoteBranchMissing")
		assert.True(t, gs.GetCanPush())
	}
}

func TestInspectTabsClose_SurvivingSiblingOutsideClosingSetSkipsPrompt(t *testing.T) {
	t.Parallel()

	t.Run("worktree with surviving sibling", func(t *testing.T) {
		t.Parallel()

		svc, d, _ := setupTestService(t)
		repoDir := initRepo(t)
		wtDir := filepath.Join(t.TempDir(), "surviving-wt")
		run(t, repoDir, "git", "worktree", "add", "-b", "feature-survive", wtDir)
		require.NoError(t, os.WriteFile(filepath.Join(wtDir, "dirty.txt"), []byte("dirty\n"), 0o644))

		wtID, err := svc.ensureTrackedWorktree(context.Background(), wtDir)
		require.NoError(t, err)
		for _, id := range []string{"agent-close-1", "agent-close-2", "agent-stay-3"} {
			createAgentForPath(t, svc, id, wtDir)
			svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, id)
		}

		w := newTestWriter()
		dispatch(d, leapmuxv1connect.WorkerGitServiceInspectTabsCloseProcedure, &leapmuxv1.InspectTabsCloseRequest{
			Tabs: []*leapmuxv1.TabIdent{
				agentTabIdent("agent-close-1"),
				agentTabIdent("agent-close-2"),
			},
		}, w)
		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)

		var resp leapmuxv1.InspectTabsCloseResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
		require.Len(t, resp.GetItems(), 2)
		for _, item := range resp.GetItems() {
			assert.False(t, item.GetShouldPrompt(), "a surviving sibling outside closingSet must skip prompting")
			assert.True(t, item.GetIsWorktree())
			assert.Nil(t, item.GetGitState(), "surviving sibling fast path skips diff/push snapshot")
			assert.Empty(t, item.GetErrorHint())
		}
	})

	t.Run("non-worktree branch with surviving sibling", func(t *testing.T) {
		t.Parallel()

		svc, d, _ := setupTestService(t)
		repoDir := initRepo(t)
		require.NoError(t, os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("dirty\n"), 0o644))
		for _, id := range []string{"branch-close-1", "branch-close-2", "branch-stay-3"} {
			createAgentForPath(t, svc, id, repoDir)
		}

		w := newTestWriter()
		dispatch(d, leapmuxv1connect.WorkerGitServiceInspectTabsCloseProcedure, &leapmuxv1.InspectTabsCloseRequest{
			Tabs: []*leapmuxv1.TabIdent{
				agentTabIdent("branch-close-1"),
				agentTabIdent("branch-close-2"),
			},
		}, w)
		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)

		var resp leapmuxv1.InspectTabsCloseResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
		require.Len(t, resp.GetItems(), 2)
		for _, item := range resp.GetItems() {
			assert.False(t, item.GetShouldPrompt())
			assert.False(t, item.GetIsWorktree())
			assert.Nil(t, item.GetGitState())
			assert.Empty(t, item.GetErrorHint())
		}
	})
}

func TestInspectTabsClose_CleanCheckoutDoesNotPrompt(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t)
	bareDir := filepath.Join(t.TempDir(), "clean-origin.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	initBareRepo(t, bareDir)

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")

	wtDir := filepath.Join(t.TempDir(), "clean-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "feature-clean", wtDir)
	run(t, wtDir, "git", "push", "-u", "origin", "feature-clean")

	wtID, err := svc.ensureTrackedWorktree(context.Background(), wtDir)
	require.NoError(t, err)
	createAgentForPath(t, svc, "agent-clean-main", repoDir)
	createAgentForPath(t, svc, "agent-clean-wt", wtDir)
	svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-clean-wt")

	// Also verify a plain non-git directory returns no prompt and no error_hint.
	plainDir := t.TempDir()
	createAgentForPath(t, svc, "agent-non-git", plainDir)

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerGitServiceInspectTabsCloseProcedure, &leapmuxv1.InspectTabsCloseRequest{
		Tabs: []*leapmuxv1.TabIdent{
			agentTabIdent("agent-clean-main"),
			agentTabIdent("agent-clean-wt"),
			agentTabIdent("agent-non-git"),
		},
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	var resp leapmuxv1.InspectTabsCloseResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetItems(), 3)

	mainItem := resp.GetItems()[0]
	assert.False(t, mainItem.GetShouldPrompt())
	assert.False(t, mainItem.GetIsWorktree())
	assert.Empty(t, mainItem.GetErrorHint())
	require.NotNil(t, mainItem.GetGitState())
	assert.False(t, mainItem.GetGitState().GetHasUncommittedChanges())
	assert.Zero(t, mainItem.GetGitState().GetUnpushedCommitCount())
	assert.False(t, mainItem.GetGitState().GetRemoteBranchMissing())

	wtItem := resp.GetItems()[1]
	assert.False(t, wtItem.GetShouldPrompt())
	assert.True(t, wtItem.GetIsWorktree())
	assert.Empty(t, wtItem.GetErrorHint())
	require.NotNil(t, wtItem.GetGitState())
	assert.False(t, wtItem.GetGitState().GetHasUncommittedChanges())
	assert.Zero(t, wtItem.GetGitState().GetUnpushedCommitCount())
	assert.False(t, wtItem.GetGitState().GetRemoteBranchMissing())

	nonGitItem := resp.GetItems()[2]
	assert.False(t, nonGitItem.GetShouldPrompt())
	assert.Empty(t, nonGitItem.GetErrorHint(), "a verified non-git directory must not fail closed")
}

func TestInspectTabsClose_NonWorktreeAllSiblingsClosingPrompts(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t)
	bareDir := filepath.Join(t.TempDir(), "branch-origin.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	initBareRepo(t, bareDir)

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")
	run(t, repoDir, "git", "checkout", "-b", "local-only-branch")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "local branch commit")
	run(t, repoDir, "git", "push", "-u", "origin", "local-only-branch")
	run(t, repoDir, "git", "push", "origin", "--delete", "local-only-branch")

	createAgentForPath(t, svc, "branch-1", repoDir)
	createAgentForPath(t, svc, "branch-2", repoDir)

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerGitServiceInspectTabsCloseProcedure, &leapmuxv1.InspectTabsCloseRequest{
		Tabs: []*leapmuxv1.TabIdent{
			agentTabIdent("branch-1"),
			agentTabIdent("branch-2"),
		},
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	var resp leapmuxv1.InspectTabsCloseResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetItems(), 2)
	for _, item := range resp.GetItems() {
		assert.True(t, item.GetShouldPrompt())
		assert.False(t, item.GetIsWorktree())
		assert.Equal(t, "local-only-branch", item.GetBranchName())
		assert.True(t, item.GetGitState().GetRemoteBranchMissing())
		assert.Empty(t, item.GetErrorHint())
	}
}

func TestInspectTabsClose_CorruptRepoSurfacesErrorHint(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t)
	brokenDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(brokenDir, ".git"), []byte("gitdir: /nonexistent/leapmux/gitdir\n"), 0o644))
	createAgentForPath(t, svc, "agent-broken", brokenDir)

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerGitServiceInspectTabsCloseProcedure, &leapmuxv1.InspectTabsCloseRequest{
		Tabs: []*leapmuxv1.TabIdent{agentTabIdent("agent-broken")},
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	var resp leapmuxv1.InspectTabsCloseResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetItems(), 1)
	assert.False(t, resp.GetItems()[0].GetShouldPrompt())
	assert.NotEmpty(t, resp.GetItems()[0].GetErrorHint(), "a corrupt git checkout must surface an error_hint so callers fail closed")
}
