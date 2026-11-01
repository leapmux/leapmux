package service

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/util/pathutil"
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

func TestWorktreeCloseIgnoresUnrelatedMissingDirectory(t *testing.T) {
	t.Parallel()
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "unrelated-missing")
	createAgentForPath(t, fx.svc, "unrelated", filepath.Join(t.TempDir(), "missing"))
	inspection, err := fx.svc.inspectLastTabClose(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, fx.tabID, "user-1")
	require.NoError(t, err)
	require.True(t, inspection.ShouldPrompt)
	require.Empty(t, inspection.ErrorHint)
	result := fx.svc.closeAgentTabCommon("user-1", fx.tabID, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED, result.WorktreeRemoval)
	assert.NoDirExists(t, fx.wtDir)
}

func TestWorktreeRemovalSerializesTabAdmission(t *testing.T) {
	t.Parallel()
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL, leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE} {
		t.Run(kind.String(), func(t *testing.T) {
			fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "admission-race")
			ctx := testutil.DeadlineContext(t)
			removalReady := make(chan struct{})
			continueRemoval := make(chan struct{})
			admissionReady := make(chan struct{})
			fx.svc.beforeWorktreeRemovalFn = func() {
				close(removalReady)
				select {
				case <-continueRemoval:
				case <-ctx.Done():
				}
			}
			fx.svc.worktreeLifecycle.beforeAdmissionFn = func() { close(admissionReady) }
			closed := make(chan *leapmuxv1.CloseTabResponse, 1)
			go func() {
				result := fx.svc.closeAgentTabCommon("user-1", fx.tabID, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
				closed <- result
			}()
			select {
			case <-removalReady:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			admitted := make(chan error, 1)
			go func() {
				switch kind {
				case leapmuxv1.TabType_TAB_TYPE_AGENT:
					dispatch(fx.d, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{WorkingDir: fx.wtDir, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}, fx.w)
					if len(fx.w.errors) == 0 {
						admitted <- nil
					} else {
						admitted <- errors.New(fx.w.errors[0].message)
					}
				case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
					dispatch(fx.d, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{WorkingDir: fx.wtDir}, fx.w)
					if len(fx.w.errors) == 0 {
						admitted <- nil
					} else {
						admitted <- errors.New(fx.w.errors[0].message)
					}
				case leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE:
					payload := fileTabPayload(filepath.Join(fx.wtDir, "README.md"), fx.wtDir)
					if kind == leapmuxv1.TabType_TAB_TYPE_IMAGE {
						payload = &leapmuxv1.TabPayload{WorkingDir: fx.wtDir, Kind: &leapmuxv1.TabPayload_Image{Image: &leapmuxv1.ImageTabPayload{AgentId: fx.tabID, Seq: 1}}}
					}
					admitted <- fx.svc.TabPayloads.Register(ctx, RegisterTabPayloadParams{UserID: "user-1", TabID: "new-payload", Payload: payload})
				case leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED:
					admitted <- errors.New("invalid test tab type")
				}
			}()
			select {
			case <-admissionReady:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			close(continueRemoval)
			select {
			case result := <-closed:
				require.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED, result.WorktreeRemoval)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case err := <-admitted:
				require.ErrorContains(t, err, "directory changed during admission")
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			rows, err := fx.svc.Queries.ListLiveTabWorkingDirs(ctx)
			require.NoError(t, err)
			assert.Empty(t, rows)
		})
	}
}

func TestWorktreeCloseIgnoresDeadLinks(t *testing.T) {
	t.Parallel()
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "dead-links")
	fx.svc.registerTabForWorktree(fx.wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "absent")
	result := fx.svc.closeAgentTabCommon("user-1", fx.tabID, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED, result.WorktreeRemoval)
	assert.NoDirExists(t, fx.wtDir)
}

func TestWorktreeCloseResolvesMissingRootLink(t *testing.T) {
	t.Parallel()
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "missing-link")
	require.NoError(t, fx.svc.Queries.DeleteWorktreeTabsByWorktreeID(context.Background(), fx.wtID))
	result := fx.svc.closeAgentTabCommon("user-1", fx.tabID, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED, result.WorktreeRemoval)
	assert.NoDirExists(t, fx.wtDir)
}

func TestUntrackedPayloadWorktreeRefusesDeletion(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	checkout := existingCheckoutForSafetyTest(t)
	wt, nested := checkout.worktree, checkout.nested
	ctx := context.Background()
	require.NoError(t, svc.TabPayloads.Register(ctx, RegisterTabPayloadParams{
		UserID: "user-1", TabID: "file", Payload: fileTabPayload(filepath.Join(nested, "committed.txt"), wt),
	}))
	inspection, err := svc.inspectLastTabClose(ctx, leapmuxv1.TabType_TAB_TYPE_FILE, "file", "user-1")
	require.NoError(t, err)
	assert.True(t, inspection.ShouldPrompt)
	assert.NotEmpty(t, inspection.WorktreeRemovalBlockedReason)
	_, err = svc.Queries.GetWorktreeByPath(ctx, pathutil.Canonicalize(wt))
	assert.ErrorIs(t, err, sql.ErrNoRows)
	result := svc.closePayloadTabCommon("user-1", "file", leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, result.WorktreeRemoval)
	assert.DirExists(t, wt)
}

func TestWorktreeTrackingKeepsItsIDAfterBackfillQueryFailure(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	ctx := context.Background()
	repo := initRepo(t)
	plan, err := svc.validateGitMode(ctx, repo, createWorktreeSetup("backfill-fault", ""))
	require.NoError(t, err)
	queries := svc.Queries
	svc.Queries = db.New(&faultingDBTX{real: svc.DB, failSubstr: "FROM tab_locations"})
	result, err := svc.executeGitMode(ctx, plan)
	require.NoError(t, err, "a backfill failure must not invalidate the stored worktree")
	require.NotEmpty(t, result.WorktreeID)
	require.Equal(t, result.WorktreeID, result.Rollback.CreatedWorktree.WorktreeID)
	svc.Queries = queries
	svc.rollbackGitMode(result)
	_, err = queries.GetWorktreeByPath(ctx, pathutil.CanonicalizeAbsent(plan.WorktreePath))
	assert.ErrorIs(t, err, sql.ErrNoRows)
	assert.NoDirExists(t, plan.WorktreePath)
}

func TestNestedReferencesBeforeRootTracking(t *testing.T) {
	t.Parallel()
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "agent", true: "terminal"}[terminal], func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			checkout := existingCheckoutForSafetyTest(t)
			wt, nested := checkout.worktree, checkout.nested
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
			closed := svc.closeAgentTabCommon("user-1", "root", leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
			assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_STILL_REFERENCED, closed.WorktreeRemoval)
			checkout.assertPreserved(t)
		})
	}
}

func TestNestedRepositoryUsesItsOwnTopLevel(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	checkout := existingCheckoutForSafetyTest(t)
	wt, nested := checkout.worktree, checkout.nested
	independent := initRepo(t)
	inner := filepath.Join(nested, "library")
	require.NoError(t, os.Rename(independent, inner))
	createAgentForPath(t, svc, "inner", inner)
	info, err := queryGitPathInfo(context.Background(), inner)
	require.NoError(t, err)
	assert.True(t, isGitTopLevel(inner, info))
	other, err := svc.hasOtherLiveWorktreeTab(context.Background(), wt, ownedTabKey{})
	require.NoError(t, err)
	assert.True(t, other, "a live nested repository must protect its enclosing directory")
	createAgentForPath(t, svc, "outer", wt)
	wtID, err := svc.ensureTrackedWorktree(context.Background(), wt)
	require.NoError(t, err)
	svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "outer")
	result := svc.closeAgentTabCommon("user-1", "outer", leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_STILL_REFERENCED, result.WorktreeRemoval)
	assert.DirExists(t, inner)
	assert.DirExists(t, filepath.Join(inner, ".git"))
}

func TestNestedUnreadableReferencePreservesCheckout(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	checkout := existingCheckoutForSafetyTest(t)
	wt, nested := checkout.worktree, checkout.nested
	createAgentForPath(t, svc, "nested-missing", filepath.Join(nested, "missing"))
	createAgentForPath(t, svc, "root", wt)
	wtID, err := svc.ensureTrackedWorktree(context.Background(), wt)
	require.NoError(t, err)
	svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "root")
	inspection, err := svc.inspectLastTabClose(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "root", "user-1")
	require.NoError(t, err)
	assert.False(t, inspection.ShouldPrompt)
	assert.NotEmpty(t, inspection.ErrorHint)
	closed := svc.closeAgentTabCommon("user-1", "root", leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, closed.WorktreeRemoval)
	checkout.assertPreserved(t)
}

func TestNestedRepositoryPayloadAndTerminalPreserveCheckout(t *testing.T) {
	t.Parallel()
	for _, file := range []bool{false, true} {
		t.Run(map[bool]string{false: "terminal", true: "file"}[file], func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			checkout := existingCheckoutForSafetyTest(t)
			repo, wt, nested := checkout.repo, checkout.worktree, checkout.nested
			inner := filepath.Join(nested, "library")
			require.NoError(t, os.Rename(initRepo(t), inner))
			if file {
				require.NoError(t, svc.TabPayloads.Register(context.Background(), RegisterTabPayloadParams{
					UserID: "user-1", TabID: "inner-file", Payload: fileTabPayload(filepath.Join(inner, "README.md"), repo),
				}))
			} else {
				createTerminalForPath(t, svc, "inner-terminal", inner)
			}
			createAgentForPath(t, svc, "outer", wt)
			wtID, err := svc.ensureTrackedWorktree(context.Background(), wt)
			require.NoError(t, err)
			svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "outer")
			result := svc.closeAgentTabCommon("user-1", "outer", leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
			assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_STILL_REFERENCED, result.WorktreeRemoval)
			assert.DirExists(t, filepath.Join(inner, ".git"))
			checkout.assertPreserved(t)
		})
	}
}
