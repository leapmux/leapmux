package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsGitTopLevel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	nested := filepath.Join(root, "frontend", "src")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	sibling := t.TempDir()
	for _, tc := range []struct {
		name, dir string
		info      *gitPathInfo
		want      bool
	}{
		{"root", root, &gitPathInfo{TopLevel: root}, true},
		{"nested", nested, &gitPathInfo{TopLevel: root}, false},
		{"sibling", sibling, &gitPathInfo{TopLevel: root}, false},
		{"empty directory", "", &gitPathInfo{TopLevel: root}, false},
		{"empty top level", root, &gitPathInfo{}, false},
		{"absent information", root, nil, false},
		{"missing directory", filepath.Join(root, "missing"), &gitPathInfo{TopLevel: root}, false},
		{"missing top level", root, &gitPathInfo{TopLevel: filepath.Join(root, "missing")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, isGitTopLevel(tc.dir, tc.info)) })
	}
	for _, dir := range []string{root, nested} {
		alias := filepath.Join(t.TempDir(), "alias")
		require.NoError(t, os.Symlink(dir, alias))
		assert.Equal(t, dir == root, isGitTopLevel(alias, &gitPathInfo{TopLevel: root}))
	}
}

func TestNestedReferencesBeforeRootTracking(t *testing.T) {
	t.Parallel()
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "agent", true: "terminal"}[terminal], func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			repo, wt, nested := existingCheckoutForSafetyTest(t)
			if terminal {
				createTerminalForPath(t, svc, "nested", nested)
			} else {
				createAgentForPath(t, svc, "nested", nested)
			}
			createAgentForPath(t, svc, "root", wt)
			result, err := svc.executeUseCurrent(context.Background(), gitModePlan{WorkingDir: wt})
			require.NoError(t, err)
			require.NotEmpty(t, result.WorktreeID)
			svc.registerTabForWorktree(result.WorktreeID, leapmuxv1.TabType_TAB_TYPE_AGENT, "root")
			inspection, err := svc.inspectLastTabClose(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "root", "user-1")
			require.NoError(t, err)
			assert.False(t, inspection.ShouldPrompt)
			// Remove the backfill to prove that removal checks live directories too.
			require.NoError(t, svc.Queries.DeleteWorktreeTabsByWorktreeID(context.Background(), result.WorktreeID))
			svc.registerTabForWorktree(result.WorktreeID, leapmuxv1.TabType_TAB_TYPE_AGENT, "root")
			closed, _ := svc.closeAgentTabCommon("user-1", "root", leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
			assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_STILL_REFERENCED, closed.WorktreeRemoval)
			assertExistingCheckoutPreserved(t, repo, wt)
		})
	}
}

func TestNestedRepositoryUsesItsOwnTopLevel(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	_, wt, nested := existingCheckoutForSafetyTest(t)
	independent := initRepo(t)
	inner := filepath.Join(nested, "library")
	require.NoError(t, os.Rename(independent, inner))
	createAgentForPath(t, svc, "inner", inner)
	info, err := queryGitPathInfo(context.Background(), inner)
	require.NoError(t, err)
	assert.True(t, isGitTopLevel(inner, info))
	other, err := svc.hasOtherLiveWorktreeTab(context.Background(), wt, ownedTabKey{})
	require.NoError(t, err)
	assert.False(t, other)
}

func TestNestedUnreadableReferencePreservesCheckout(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo, wt, nested := existingCheckoutForSafetyTest(t)
	createAgentForPath(t, svc, "nested-missing", filepath.Join(nested, "missing"))
	createAgentForPath(t, svc, "root", wt)
	wtID, err := svc.ensureTrackedWorktree(context.Background(), wt)
	require.NoError(t, err)
	svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "root")
	inspection, err := svc.inspectLastTabClose(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "root", "user-1")
	require.NoError(t, err)
	assert.False(t, inspection.ShouldPrompt)
	assert.NotEmpty(t, inspection.ErrorHint)
	closed, _ := svc.closeAgentTabCommon("user-1", "root", leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, closed.WorktreeRemoval)
	assertExistingCheckoutPreserved(t, repo, wt)
}
