package service

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/util/pathutil"
)

// --- Fixture ---------------------------------------------------------
//
// closeTabFixture bundles the setup common to the "happy path" close
// tests: a service with one workspace, a real git repo + worktree on a
// fresh branch, a registered agent or terminal, and a tab→worktree
// association. The fixture registers drainAllInFlight via t.Cleanup *after*
// its own t.TempDir() calls, so LIFO cleanup ordering drains in-flight
// handlers before any fixture-owned disk is removed.

type closeTabFixture struct {
	svc   *Service
	d     *channel.Dispatcher
	w     *testResponseWriter
	wtDir string
	wtID  string
	tabID string
}

func setupCloseTabFixture(t *testing.T, tabType leapmuxv1.TabType, scenario string) closeTabFixture {
	t.Helper()
	svc, d, w := setupTestService(t)

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), scenario+"-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", scenario, wtDir)

	wtID, err := svc.ensureTrackedWorktree(context.Background(), wtDir)
	require.NoError(t, err)

	tabID := scenario + "-tab"
	switch tabType {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		createAgentForPath(t, svc, tabID, wtDir)
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		require.NoError(t, svc.Queries.UpsertTerminal(context.Background(), db.UpsertTerminalParams{
			ID:         tabID,
			WorkingDir: wtDir,
			Screen:     []byte{},
		}))
	default:
		t.Fatalf("unsupported tab type: %v", tabType)
	}
	svc.registerTabForWorktree(wtID, tabType, tabID)

	t.Cleanup(func() { drainAllInFlight(svc) })

	return closeTabFixture{svc: svc, d: d, w: w, wtDir: wtDir, wtID: wtID, tabID: tabID}
}

// --- CloseTab of an AGENT tab -------------------------------------------------------

func TestCloseAgent_WithWorktreeActionRemove_RemovesWorktreeSync(t *testing.T) {
	t.Parallel()

	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "close-remove")

	dispatch(fx.d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab:            agentTabIdent(fx.tabID),
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	}, fx.w)

	require.Empty(t, fx.w.errors, "unexpected RPC errors: %+v", fx.w.errors)
	require.Len(t, fx.w.responses, 1)
	var resp leapmuxv1.CloseTabResponse
	require.NoError(t, proto.Unmarshal(fx.w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetFailureMessage(), "expected success, got failure: %s / %s", resp.GetFailureMessage(), resp.GetFailureDetail())
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED, resp.GetWorktreeRemoval(), "last-reference close should report REMOVED")

	// Worktree directory gone.
	_, statErr := os.Stat(fx.wtDir)
	assert.True(t, os.IsNotExist(statErr), "expected worktree dir removed, stat err: %v", statErr)

	// DB row soft-deleted.
	row, err := fx.svc.Queries.GetWorktreeByID(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.True(t, row.DeletedAt.Valid, "expected DB row to be soft-deleted")
}

func TestRemoveWorktreeFromDiskSerializesBranchSelection(t *testing.T) {
	t.Parallel()
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "branch-selection")
	ctx := context.Background()
	wt, err := fx.svc.Queries.GetWorktreeByID(ctx, fx.wtID)
	require.NoError(t, err)
	require.NoError(t, gitutil.Run(ctx, wt.RepoRoot, "branch", "alternate"))
	checkoutEntered := false
	fx.svc.afterWorktreeBranchProbeFn = func() {
		lock := fx.svc.gitIndexLock(wt.RepoRoot)
		if lock.TryLock() {
			defer lock.Unlock()
			checkoutEntered = true
			require.NoError(t, gitutil.Run(ctx, fx.wtDir, "checkout", "alternate"))
		}
	}
	require.NoError(t, fx.svc.removeWorktreeFromDisk(wt))
	assert.False(t, checkoutEntered, "a checkout must not enter after removal selects its branch")
	if checkoutEntered {
		assert.True(t, localBranchExists(t, wt.RepoRoot, wt.BranchName), "removal must retain a branch that the worktree left")
		assert.False(t, localBranchExists(t, wt.RepoRoot, "alternate"), "removal must delete the current branch")
	} else {
		assert.False(t, localBranchExists(t, wt.RepoRoot, wt.BranchName))
		assert.True(t, localBranchExists(t, wt.RepoRoot, "alternate"))
	}
}

// TestCloseAgent_WithWorktreeActionRemove_RefusesALockedWorktree pins the
// removal policy at the site that PERFORMS the removal, and not only at the
// three clients that offer the choice.
//
// A client that ignores worktree_removal_blocked_reason still reaches this
// line, and a `git worktree lock` taken after the dialog opened beats any
// answer the client was given -- the check here runs inside the per-worktree
// removal lock, so it cannot be raced. The close request below carries REMOVE
// with no client-side preflight at all, which is exactly that case.
func TestCloseAgent_WithWorktreeActionRemove_RefusesALockedWorktree(t *testing.T) {
	t.Parallel()

	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "close-locked")
	run(t, fx.wtDir, "git", "worktree", "lock", "--reason", "held for review", fx.wtDir)

	dispatch(fx.d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab:            agentTabIdent(fx.tabID),
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	}, fx.w)

	require.Empty(t, fx.w.errors)
	require.Len(t, fx.w.responses, 1)
	var resp leapmuxv1.CloseTabResponse
	require.NoError(t, proto.Unmarshal(fx.w.responses[0].GetPayload(), &resp))

	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, resp.GetWorktreeRemoval())
	assert.Equal(t, "Worktree kept: git refuses to remove it", resp.GetFailureMessage(),
		"the refusal names git, not unsaved work -- the lock is what kept the directory")
	// The curated sentence, not git's raw stderr. One source states the reason,
	// so every surface says the same thing and every one of them is actionable.
	assert.Contains(t, resp.GetFailureDetail(), "held for review")
	assert.Contains(t, resp.GetFailureDetail(), "git worktree unlock")
	assert.NotContains(t, resp.GetFailureDetail(), "fatal:")

	// Nothing was destroyed, and the row survives to describe what is still there.
	assert.DirExists(t, fx.wtDir)
	row, err := fx.svc.Queries.GetWorktreeByID(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.False(t, row.DeletedAt.Valid, "a refused removal must not tombstone the row")
}

func TestCloseAgent_WithWorktreeActionKeep_PreservesWorktree(t *testing.T) {
	t.Parallel()

	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "close-keep")

	dispatch(fx.d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab:            agentTabIdent(fx.tabID),
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP,
	}, fx.w)

	require.Empty(t, fx.w.errors)
	require.Len(t, fx.w.responses, 1)

	// Worktree dir survives.
	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr, "expected worktree dir preserved")

	// DB row still present and NOT soft-deleted.
	row, err := fx.svc.Queries.GetWorktreeByID(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.False(t, row.DeletedAt.Valid, "worktree DB row should not be soft-deleted under KEEP")

	// Tab association dropped.
	count, err := fx.svc.Queries.CountWorktreeTabs(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestCloseAgent_WithWorktreeActionUnspecified_PreservesWorktree(t *testing.T) {
	t.Parallel()

	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "close-default")

	// No worktree_action set = UNSPECIFIED — should behave like KEEP.
	dispatch(fx.d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: agentTabIdent(fx.tabID)}, fx.w)

	require.Empty(t, fx.w.errors)
	require.Len(t, fx.w.responses, 1)

	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr, "expected worktree dir preserved by default")
}

func TestCloseAgent_NoWorktree_Succeeds(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	// Agent without any worktree association.
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		ID:         "agent-noworktree",
		WorkingDir: t.TempDir(),
	}))

	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab:            agentTabIdent("agent-noworktree"),
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.CloseTabResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetFailureMessage())
}

func TestCloseAgent_WorktreeRemove_OtherTabsStillOpen_PreservesWorktree(t *testing.T) {
	t.Parallel()

	// This test extends the fixture with a second agent on the same
	// worktree; the fixture normally registers only fx.tabID. That
	// sibling is what prevents worktree removal even though
	// the close request asks for REMOVE.
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "close-shared")
	const siblingID = "close-shared-sibling"
	createAgentForPath(t, fx.svc, siblingID, fx.wtDir)
	fx.svc.registerTabForWorktree(fx.wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, siblingID)

	dispatch(fx.d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab:            agentTabIdent(fx.tabID),
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	}, fx.w)

	require.Empty(t, fx.w.errors)
	require.Len(t, fx.w.responses, 1)
	var resp leapmuxv1.CloseTabResponse
	require.NoError(t, proto.Unmarshal(fx.w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetFailureMessage())
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_STILL_REFERENCED, resp.GetWorktreeRemoval(), "a close that leaves siblings should report STILL_REFERENCED")

	// Other tab still references the worktree — it must survive.
	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr, "worktree must survive while other tabs reference it")

	count, err := fx.svc.Queries.CountWorktreeTabs(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "exactly one tab should remain linked")
}

func TestCloseTabCommon_ConcurrentRemove_SerializesToSingleRemoval(t *testing.T) {
	t.Parallel()

	// Concurrent closes must run Git removal once, even when both rows close before the final scan.
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "close-concurrent")
	const siblingID = "close-concurrent-sibling"
	createAgentForPath(t, fx.svc, siblingID, fx.wtDir)
	fx.svc.registerTabForWorktree(fx.wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, siblingID)

	var removals atomic.Int32
	fx.svc.beforeWorktreeRemovalFn = func() { removals.Add(1) }
	var wg sync.WaitGroup
	results := make([]*leapmuxv1.CloseTabResponse, 2)
	for i, id := range []string{fx.tabID, siblingID} {
		wg.Add(1)
		go func(idx int, agentID string) {
			defer wg.Done()
			results[idx] = closeAgentRemoveDirect(fx.svc, agentID)
		}(i, id)
	}
	wg.Wait()

	assert.Equal(t, int32(1), removals.Load(), "only one close runs Git removal")
	for _, r := range results {
		assert.Empty(t, r.GetFailureMessage(), "neither concurrent close should fail")
		assert.Contains(t, []leapmuxv1.WorktreeRemovalOutcome{
			leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED,
			leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_STILL_REFERENCED,
		}, r.GetWorktreeRemoval())
	}

	// Worktree removed (once), DB row soft-deleted.
	_, statErr := os.Stat(fx.wtDir)
	assert.True(t, os.IsNotExist(statErr), "worktree dir should be removed")
	row, err := fx.svc.Queries.GetWorktreeByID(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.True(t, row.DeletedAt.Valid, "DB row soft-deleted")
}

// faultingDBTX wraps a real db.DBTX and injects a failure for any
// statement whose SQL contains failSubstr. Exec/Query faults return the
// error directly; a QueryRow fault is redirected to an invalid query so
// the deferred Scan fails with a non-ErrNoRows error. Used to drive the
// partial-failure (WORKTREE_REMOVAL_OUTCOME_FAILED) branches of
// closeTabCommon that an in-memory SQLite never reaches on its own.
type faultingDBTX struct {
	real       db.DBTX
	failSubstr string
}

var errInjectedDBFault = errors.New("injected DB fault")

func (f *faultingDBTX) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	if strings.Contains(query, f.failSubstr) {
		return nil, errInjectedDBFault
	}
	return f.real.ExecContext(ctx, query, args...)
}

func (f *faultingDBTX) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return f.real.PrepareContext(ctx, query)
}

func (f *faultingDBTX) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	if strings.Contains(query, f.failSubstr) {
		return nil, errInjectedDBFault
	}
	return f.real.QueryContext(ctx, query, args...)
}

func (f *faultingDBTX) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	if strings.Contains(query, f.failSubstr) {
		// Redirect to a query that errors on Scan with a non-ErrNoRows
		// error, so the caller treats it as a real DB failure rather than
		// "no such row" (which would degrade REMOVE to KEEP).
		return f.real.QueryRowContext(ctx, "SELECT nonexistent_column")
	}
	return f.real.QueryRowContext(ctx, query, args...)
}

// closeAgentRemoveDirect drives closeTabCommon for an AGENT REMOVE close
// against the current svc.Queries, mirroring how the CloseTab handler
// invokes it. Returns the partial-failure result so a test can assert
// the WorktreeRemovalOutcome without unmarshaling an RPC response.
func closeAgentRemoveDirect(svc *Service, agentID string) *leapmuxv1.CloseTabResponse {
	return svc.closeTabCommon(
		leapmuxv1.TabType_TAB_TYPE_AGENT,
		agentID,
		"",
		leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
		dropWorktreeLink,
		func() {},
		func() (bool, error) { return rowsAffected(svc.Queries.CloseAgent(bgCtx(), agentID)) },
	)
}

func TestCloseTabCommon_LinkDropFailure_ReportsFailedNotStillReferenced(t *testing.T) {
	t.Parallel()

	// A failed link deletion must reach the caller and preserve the directory.
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "drop-fail")
	realQueries := fx.svc.Queries
	defer func() { fx.svc.Queries = realQueries }()
	// RemoveWorktreeTab is the only statement matching this substring;
	// DeleteWorktreeTabsByTabID filters on `tab_type`, not `worktree_id`.
	fx.svc.Queries = db.New(&faultingDBTX{real: fx.svc.DB, failSubstr: "DELETE FROM worktree_tabs WHERE worktree_id"})

	result := closeAgentRemoveDirect(fx.svc, fx.tabID)

	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, result.GetWorktreeRemoval(), "a link-drop failure must report FAILED, not STILL_REFERENCED")
	assert.Equal(t, "Failed to remove worktree", result.GetFailureMessage())
	// ensureTrackedWorktree stores the canonicalized path, so compare
	// against that (fx.wtDir is the pre-symlink-resolution /tmp form).
	assert.Equal(t, pathutil.Canonicalize(fx.wtDir), result.GetWorktreePath(), "failure must carry the worktree path for manual cleanup")
	assert.Equal(t, fx.wtID, result.GetWorktreeId())

	// The worktree dir must survive — we never reached removeWorktreeFromDisk.
	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr, "worktree dir must remain when the close failed before removal")
}

func TestCloseTabCommon_CountFailure_ReportsFailed(t *testing.T) {
	t.Parallel()

	// A failed live-tab query after the link is dropped means
	// we can't tell whether siblings remain, so we can't safely remove.
	// Surface FAILED with the path rather than a clean result.
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "count-fail")
	realQueries := fx.svc.Queries
	defer func() { fx.svc.Queries = realQueries }()
	fx.svc.Queries = db.New(&faultingDBTX{real: fx.svc.DB, failSubstr: "FROM tab_locations"})

	result := closeAgentRemoveDirect(fx.svc, fx.tabID)

	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, result.GetWorktreeRemoval())
	assert.Equal(t, "Failed to remove worktree", result.GetFailureMessage())
	assert.Equal(t, pathutil.Canonicalize(fx.wtDir), result.GetWorktreePath())
	assert.Equal(t, fx.wtID, result.GetWorktreeId())

	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr, "worktree dir must remain when the count failed")
}

func TestCloseTabCommon_WorktreeLookupFailure_ReportsFailedAndKeepsLink(t *testing.T) {
	t.Parallel()

	// A failed worktree lookup prevents removal and preserves the link for database cleanup.
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "lookup-fail")
	realQueries := fx.svc.Queries
	fx.svc.Queries = db.New(&faultingDBTX{real: fx.svc.DB, failSubstr: "JOIN worktree_tabs"})

	result := closeAgentRemoveDirect(fx.svc, fx.tabID)

	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, result.GetWorktreeRemoval())
	assert.Equal(t, "Failed to check worktree for removal", result.GetFailureMessage())

	// Restore the real queries to inspect the resulting state.
	fx.svc.Queries = realQueries

	// The tab itself was still closed (closeDB ran before the link drop)...
	agent, err := fx.svc.getAgentByID(context.Background(), fx.tabID)
	require.NoError(t, err)
	assert.True(t, agent.ClosedAt.Valid, "the tab itself should still be closed")

	// ...but its worktree link survives as a strand the GC can reap, rather
	// than being dropped into a zero-link, un-GC-able orphan.
	count, err := fx.svc.Queries.CountWorktreeTabs(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "the closed tab's link must survive as a GC strand")
	live, err := fx.svc.Queries.CountLiveWorktreeRefs(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), live, "no live ref remains, so the orphan GC can reclaim it")
}

func TestRemoveWorktreeIfLastReference_RowRemovedUnderLock_SkipsDiskRemoval(t *testing.T) {
	t.Parallel()

	// Models the TOCTOU the under-lock re-read defends: wt is resolved before
	// the per-worktree lock, but a concurrent close / the orphan GC soft-deletes
	// the row in the meantime. We must NOT re-run `git worktree remove` -- the
	// path may now be owned by a freshly-adopted worktree under a new id (and a
	// different lock) -- but instead drop our dead link and report the terminal
	// REMOVED state.
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "toctou")
	stale, err := fx.svc.Queries.GetWorktreeByID(context.Background(), fx.wtID)
	require.NoError(t, err)
	// Another actor already removed it: soft-delete the row out from under our
	// stale `stale` copy. Leave the on-disk dir in place so a spurious
	// removeWorktreeFromDisk would be observable (the dir would vanish) if the
	// re-read failed to bail.
	require.NoError(t, fx.svc.Queries.DeleteWorktree(context.Background(), fx.wtID))

	result := &leapmuxv1.CloseTabResponse{}
	fx.svc.removeWorktreeIfLastReference(result, &stale, leapmuxv1.TabType_TAB_TYPE_AGENT, fx.tabID, "", true, fx.wtDir)

	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED, result.GetWorktreeRemoval(),
		"an already-removed row reports the terminal REMOVED state")
	assert.Empty(t, result.GetFailureMessage())
	// The dir must survive -- we must not have re-run `git worktree remove`.
	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr, "re-read must skip removeWorktreeFromDisk for an already-removed row")
	// Our dead link was dropped so it isn't left as a strand.
	count, err := fx.svc.Queries.CountWorktreeTabs(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "the close drops its now-dead link")
}

func TestRemoveWorktreeIfLastReference_RereadError_ReportsFailed(t *testing.T) {
	t.Parallel()

	// If the under-lock re-read of the worktree row errors (a transient DB
	// fault, not ErrNoRows), we can't confirm whether the row is still ours
	// to remove, so we must NOT blindly run `git worktree remove`. Surface a
	// partial failure instead. `GetWorktreeForTab` (a JOIN) still succeeds;
	// only the bare `... FROM worktrees WHERE id = ?` re-read faults.
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "reread-fail")
	realQueries := fx.svc.Queries
	defer func() { fx.svc.Queries = realQueries }()
	fx.svc.Queries = db.New(&faultingDBTX{real: fx.svc.DB, failSubstr: "FROM worktrees WHERE id = ?"})

	result := closeAgentRemoveDirect(fx.svc, fx.tabID)

	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, result.GetWorktreeRemoval(),
		"a re-read error must report FAILED, not blindly remove")
	assert.Equal(t, "Failed to remove worktree", result.GetFailureMessage())
	// The dir must survive -- we bailed before removeWorktreeFromDisk.
	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr, "a re-read error must not remove the worktree")
}

func TestCleanupOrphanWorktreeLinks_PreservesDirectoryAndDropsDeadLinks(t *testing.T) {
	t.Parallel()

	// Cleanup removes the link to the closed agent and preserves its Git artifacts.
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "reap-strand")
	require.NoError(t, closeErr(fx.svc.Queries.CloseAgent(context.Background(), fx.tabID)))
	wt, err := fx.svc.Queries.GetWorktreeByID(context.Background(), fx.wtID)
	require.NoError(t, err)

	require.NoError(t, fx.svc.CleanupOrphanWorktreeLinks(context.Background(), wt))

	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr, "cleanup preserves the directory")
	row, err := fx.svc.Queries.GetWorktreeByID(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.False(t, row.DeletedAt.Valid, "cleanup preserves the tracking row")
	count, err := fx.svc.Queries.CountWorktreeTabs(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "strand links dropped")
}

func TestCleanupOrphanWorktreeLinks_PreservesUncommittedWork(t *testing.T) {
	t.Parallel()

	// Uncommitted work survives cleanup, just as a clean checkout does.
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "reap-dirty")
	require.NoError(t, closeErr(fx.svc.Queries.CloseAgent(context.Background(), fx.tabID)))
	require.NoError(t, os.WriteFile(filepath.Join(fx.wtDir, "uncommitted.txt"), []byte("work in progress\n"), 0o600))
	wt, err := fx.svc.Queries.GetWorktreeByID(context.Background(), fx.wtID)
	require.NoError(t, err)

	require.NoError(t, fx.svc.CleanupOrphanWorktreeLinks(context.Background(), wt))

	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr, "a worktree with uncommitted changes must survive the reap")
	row, err := fx.svc.Queries.GetWorktreeByID(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.False(t, row.DeletedAt.Valid, "cleanup must preserve the tracking record")
	// Dead links no longer require another cleanup pass.
	count, err := fx.svc.Queries.CountWorktreeTabs(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.Zero(t, count, "cleanup removes dead links")
}

func TestCleanupOrphanWorktreeLinks_SkipsWhenLiveRefRemains(t *testing.T) {
	t.Parallel()

	// The cleanup statement checks liveness again and preserves the live link.
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "reap-live")
	wt, err := fx.svc.Queries.GetWorktreeByID(context.Background(), fx.wtID)
	require.NoError(t, err)

	require.NoError(t, fx.svc.CleanupOrphanWorktreeLinks(context.Background(), wt))

	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr, "a worktree with a live ref must not be removed")
	row, err := fx.svc.Queries.GetWorktreeByID(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.False(t, row.DeletedAt.Valid, "row must remain")
}

func TestCloseWorktreeDispositionFor(t *testing.T) {
	t.Parallel()

	// The mapping every close-during-startup decision flows through. A
	// convergence close pins the action to UNSPECIFIED, so the link policy --
	// not the action -- is what separates "the workspace is gone, let the
	// reconciler reclaim it" from "keep the directory".
	tests := []struct {
		name   string
		action leapmuxv1.WorktreeAction
		policy worktreeLinkPolicy
		want   closeWorktreeDisposition
	}{
		{"user KEEP", leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP, dropWorktreeLink, keepWorktreeOnClose},
		{"user REMOVE", leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink, removeWorktreeOnClose},
		{"unspecified is keep", leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, dropWorktreeLink, keepWorktreeOnClose},
		{"deleted workspace strands", leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, keepWorktreeLinkForReconciler, strandWorktreeOnClose},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, closeWorktreeDispositionFor(tt.action, tt.policy))
		})
	}
}

func TestRegisterTabForWorktreeAfterClose(t *testing.T) {
	t.Parallel()

	// Pins the skip-vs-link decision shared by runAgentStartup /
	// runTerminalStartup. A tab that survived startup is linked normally; a
	// tab closed during startup is linked only when the close was a deleted
	// workspace's, because there the strand is what makes the worktree an
	// orphan-GC candidate.
	svc, _, _ := setupTestService(t)
	q := svc.Queries
	ctx := context.Background()
	_, cwErr := q.CreateWorktree(ctx, db.CreateWorktreeParams{ID: "wt-1", WorktreePath: "/r/wt-1", RepoRoot: "/r", BranchName: "b"})
	require.NoError(t, cwErr)

	// Closed during startup under KEEP: skip the link, so the worktree ends at
	// zero links -- exactly what an online KEEP close of a started tab leaves.
	svc.registerTabForWorktreeAfterClose("wt-1", leapmuxv1.TabType_TAB_TYPE_AGENT, "a-keep", true, keepWorktreeOnClose)
	count, err := q.CountWorktreeTabs(ctx, "wt-1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "a KEEP close during startup must not link the tab")

	// Closed during startup under REMOVE: also skip -- the caller rolls the
	// worktree back instead, so a link would only strand a row.
	svc.registerTabForWorktreeAfterClose("wt-1", leapmuxv1.TabType_TAB_TYPE_AGENT, "a-remove", true, removeWorktreeOnClose)
	count, err = q.CountWorktreeTabs(ctx, "wt-1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "a REMOVE close during startup must not link the tab")

	// Closed during startup by a deleted workspace: link deliberately, so the
	// orphan reconciler sees a candidate.
	svc.registerTabForWorktreeAfterClose("wt-1", leapmuxv1.TabType_TAB_TYPE_AGENT, "a-strand", true, strandWorktreeOnClose)
	count, err = q.CountWorktreeTabs(ctx, "wt-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "a deleted workspace's close must strand the link for the reconciler")

	// Not closed (the common case, and the transient-fetch-error
	// fall-through): link the tab regardless of disposition.
	svc.registerTabForWorktreeAfterClose("wt-1", leapmuxv1.TabType_TAB_TYPE_AGENT, "a-open", false, keepWorktreeOnClose)
	count, err = q.CountWorktreeTabs(ctx, "wt-1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), count, "a tab that survived startup must be linked")

	// Empty worktree id is a no-op, inherited from registerTabForWorktree.
	svc.registerTabForWorktreeAfterClose("", leapmuxv1.TabType_TAB_TYPE_TERMINAL, "t-1", false, keepWorktreeOnClose)
	count, err = q.CountWorktreeTabs(ctx, "wt-1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), count, "an empty worktree id links nothing")
}

func TestEnsureTrackedWorktree_BackfillsExistingFileTab(t *testing.T) {
	t.Parallel()

	// A FILE tab opened before its worktree was tracked starts unlinked
	// (linkTabToWorktree found no worktrees row at registration time).
	// When the worktree is later adopted, ensureTrackedWorktree's backfill
	// must link the pre-existing file tab so it ref-counts the worktree —
	// otherwise a sibling close could `git worktree remove` the dir while
	// that editor is still open.
	svc, _, _ := setupTestService(t)
	svc.TabPayloads = NewTabPayloadStore(svc.Queries, nil, &svc.worktreeLifecycle)

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "adopt-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "adopt", wtDir)

	// Register a FILE tab inside the worktree BEFORE the worktree row
	// exists, so linkTabToWorktree finds no row and leaves it unlinked.
	filePath := filepath.Join(wtDir, "file.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o644))
	require.NoError(t, svc.TabPayloads.Register(context.Background(), RegisterTabPayloadParams{
		UserID:  "user-1",
		TabID:   "adopt-file-tab",
		Payload: fileTabPayload(filePath, ""),
	}))

	// Adopt the worktree: creates the row and runs the backfill.
	wtID, err := svc.ensureTrackedWorktree(context.Background(), wtDir)
	require.NoError(t, err)

	count, err := svc.Queries.CountWorktreeTabs(context.Background(), wtID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "the pre-existing file tab should be backfilled onto the adopted worktree")

	// Prove the backfilled link prevents removal — the whole point
	// of the backfill. Open an AGENT in the same worktree, then close it
	// with REMOVE: the backfilled FILE link must keep the count above zero
	// so the worker reports STILL_REFERENCED and leaves the dir on disk
	// (rather than `git worktree remove`-ing it out from under the open
	// editor).
	createAgentForPath(t, svc, "adopt-agent", wtDir)
	svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "adopt-agent")

	result := closeAgentRemoveDirect(svc, "adopt-agent")
	assert.Empty(t, result.GetFailureMessage())
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_STILL_REFERENCED, result.GetWorktreeRemoval(),
		"the backfilled FILE link must keep the worktree referenced when the AGENT closes")
	_, statErr := os.Stat(wtDir)
	assert.NoError(t, statErr, "worktree dir must survive while the backfilled FILE tab still references it")
	count, err = svc.Queries.CountWorktreeTabs(context.Background(), wtID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "only the FILE link should remain after the AGENT close")
}

func TestCloseAgent_WorktreeRemoveFailure_ReturnsFailureMessage(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	// Create a worktree DB row pointing at a path that is NOT a git
	// worktree — the row a `mv` of the directory leaves behind. The removal
	// preflight answers for git up front (worktreeRemovalBlockedReason): it
	// sees git does not list the path and refuses the close, so `git
	// worktree remove` never runs and the refusal, not git's stderr, is the
	// failure the close must report.
	//
	// The row is stored CANONICALIZED, as ensureTrackedWorktreeWith stores
	// every production row. worktreeListingFor's DB fallback looks the row
	// up by CanonicalizeAbsent, so a raw-spelled path (t.TempDir under
	// macOS's /var symlink) dodges the fallback, fails the preflight open,
	// and the removal reaches `git worktree remove` — a different outcome
	// per platform off one setup.
	repoDir := initRepo(t)
	// Canonicalize only after the directory exists: Canonicalize falls back
	// to Clean while the path is absent, which on macOS keeps the /var
	// spelling rather than resolving to /private/var.
	rawPath := filepath.Join(t.TempDir(), "not-a-worktree")
	require.NoError(t, os.MkdirAll(rawPath, 0o755))
	bogusPath := pathutil.Canonicalize(rawPath)
	wtID := "wt-bogus"
	_, cwErr := svc.Queries.CreateWorktree(context.Background(), db.CreateWorktreeParams{
		ID:           wtID,
		WorktreePath: bogusPath,
		RepoRoot:     repoDir,
		BranchName:   "",
	})
	require.NoError(t, cwErr)

	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		ID:         "agent-fail",
		WorkingDir: bogusPath,
	}))
	svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-fail")

	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab:            agentTabIdent("agent-fail"),
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.CloseTabResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Equal(t, "Worktree kept: deletion was not authorized", resp.GetFailureMessage(),
		"an invalid root cannot authorize deletion")
	assert.Contains(t, resp.GetFailureDetail(), "linked worktree top level",
		"detail should state the preflight's refusal, not git's raw stderr")
	assert.Equal(t, bogusPath, resp.GetWorktreePath(), "the path the user must clean up by hand rides the structured field")
	assert.Equal(t, wtID, resp.GetWorktreeId())
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, resp.GetWorktreeRemoval(), "a failed removal should report FAILED")

	// Disk orphan remains.
	_, statErr := os.Stat(bogusPath)
	assert.NoError(t, statErr, "disk path should still exist when removal failed")

	// The row OUTLIVES a removal that did not happen. It is the only handle
	// anything keeps on the directory: ListOrphanCandidateWorktrees filters
	// `deleted_at IS NULL`, so a tombstone here strands the orphan forever and
	// no later repair (a `git worktree unlock`, a fixed admin dir) can bring it
	// back as a GC candidate.
	row, err := svc.Queries.GetWorktreeByID(context.Background(), wtID)
	require.NoError(t, err)
	assert.False(t, row.DeletedAt.Valid, "a failed removal must preserve the tracking record")
}

func TestCloseAgent_WorktreeRemove_MissingPathRefusesDeletion(t *testing.T) {
	t.Parallel()

	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "close-gone")

	// A missing directory cannot authorize deletion of its Git registration or branch.
	require.NoError(t, os.RemoveAll(fx.wtDir))

	dispatch(fx.d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab:            agentTabIdent(fx.tabID),
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	}, fx.w)

	require.Empty(t, fx.w.errors)
	require.Len(t, fx.w.responses, 1)
	var resp leapmuxv1.CloseTabResponse
	require.NoError(t, proto.Unmarshal(fx.w.responses[0].GetPayload(), &resp))
	assert.NotEmpty(t, resp.GetFailureMessage(), "an absent path cannot authorize deletion")

	row, err := fx.svc.Queries.GetWorktreeByID(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.False(t, row.DeletedAt.Valid, "an absent path preserves the tracking row")
}

func TestCloseAgent_AlreadyClosed_Idempotent(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		ID:         "agent-idem",
		WorkingDir: t.TempDir(),
	}))

	// First close.
	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: agentTabIdent("agent-idem")}, w)
	require.Empty(t, w.errors)

	// Second close — requireAccessibleAgent should reject because the
	// agent is now closed. That's acceptable; the key property is the
	// handler does not wedge the Cleanup wait-group.
	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: agentTabIdent("agent-idem")}, w)
	// Accept either a benign error or a success response.
	// What we actually require: Cleanup wait-group is not stuck.
	done := make(chan struct{})
	go func() {
		svc.Cleanup.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Cleanup.Wait did not return after double close")
	}
}

// --- CloseTab of a TERMINAL tab --------------------------------------------

func TestCloseTerminal_WithWorktreeActionRemove_RemovesWorktreeSync(t *testing.T) {
	t.Parallel()

	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, "t-close-remove")

	dispatch(fx.d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab:            terminalTabIdent(fx.tabID),
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	}, fx.w)

	require.Empty(t, fx.w.errors)
	require.Len(t, fx.w.responses, 1)
	var resp leapmuxv1.CloseTabResponse
	require.NoError(t, proto.Unmarshal(fx.w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetFailureMessage())

	_, statErr := os.Stat(fx.wtDir)
	assert.True(t, os.IsNotExist(statErr))
}

func TestCloseTerminal_WithWorktreeActionUnspecified_PreservesWorktree(t *testing.T) {
	t.Parallel()

	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, "t-close-default")

	// No worktree_action = UNSPECIFIED, treated as KEEP.
	dispatch(fx.d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab: terminalTabIdent(fx.tabID),
	}, fx.w)

	require.Empty(t, fx.w.errors)
	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr, "expected worktree dir preserved by default")
}

func TestCloseTerminal_NoWorktree_Succeeds(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	require.NoError(t, svc.Queries.UpsertTerminal(context.Background(), db.UpsertTerminalParams{
		ID:         "term-noworktree",
		WorkingDir: t.TempDir(),
		Screen:     []byte{},
	}))

	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab:            terminalTabIdent("term-noworktree"),
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.CloseTabResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetFailureMessage())
}

func TestCloseTerminal_WithWorktreeActionKeep_PreservesWorktree(t *testing.T) {
	t.Parallel()

	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, "t-close-keep")

	dispatch(fx.d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab:            terminalTabIdent(fx.tabID),
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP,
	}, fx.w)

	require.Empty(t, fx.w.errors)
	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr)
}

// --- removeWorktreeFromDisk direct coverage ---------------------------

func TestRemoveWorktreeFromDisk_Success_ReturnsNil(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "rwtfd-ok")
	run(t, repoDir, "git", "worktree", "add", "-b", "rwtfd-ok", wtDir)

	wtID, err := svc.ensureTrackedWorktree(context.Background(), wtDir)
	require.NoError(t, err)
	wt, err := svc.Queries.GetWorktreeByID(context.Background(), wtID)
	require.NoError(t, err)

	err = svc.removeWorktreeFromDisk(wt)
	assert.NoError(t, err)
	_, statErr := os.Stat(wtDir)
	assert.True(t, os.IsNotExist(statErr))

	// The post-`git worktree remove` cleanup fans out two independent
	// goroutines (branch -D and DB DeleteWorktree). Both must run; the
	// earlier serial implementation called them in sequence so a
	// regression that skipped either would slip past the path-removed
	// assertion above.
	assert.False(t, localBranchExists(t, repoDir, "rwtfd-ok"), "unused branch should be deleted after worktree remove")
	row, err := svc.Queries.GetWorktreeByID(context.Background(), wtID)
	require.NoError(t, err)
	assert.True(t, row.DeletedAt.Valid, "DB row soft-deleted on success path")
}

// TestRemoveWorktreeFromDisk_BranchInUse_KeepsBranch verifies the
// IsBranchInUse guard that blocks `git branch -D`: if another worktree is
// still on the branch, the branch must survive. Pins the in-use → keep
// contract through the post-parallelization code path.
func TestRemoveWorktreeFromDisk_BranchInUse_KeepsBranch(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	// Two worktrees, distinct paths, both on the same branch — git
	// allows this only via `add --force`. The branch outlives the first
	// removal because the second worktree is still on it.
	branch := "rwtfd-shared"
	wtA := filepath.Join(t.TempDir(), "rwtfd-shared-a")
	wtB := filepath.Join(t.TempDir(), "rwtfd-shared-b")
	run(t, repoDir, "git", "worktree", "add", "-b", branch, wtA)
	run(t, repoDir, "git", "worktree", "add", "--force", wtB, branch)

	wtIDA, err := svc.ensureTrackedWorktree(context.Background(), wtA)
	require.NoError(t, err)
	wtA_row, err := svc.Queries.GetWorktreeByID(context.Background(), wtIDA)
	require.NoError(t, err)

	require.NoError(t, svc.removeWorktreeFromDisk(wtA_row))
	assert.True(t, localBranchExists(t, repoDir, branch), "branch must survive when another worktree still uses it")
	_, statErr := os.Stat(wtA)
	assert.True(t, os.IsNotExist(statErr), "wtA path removed")
	// wtB is untouched.
	_, statErr = os.Stat(wtB)
	assert.NoError(t, statErr, "wtB path still on disk")
}

func TestRemoveWorktreeFromDisk_GitFailure_PathIntact_ReturnsError(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	bogusPath := filepath.Join(t.TempDir(), "rwtfd-fail")
	require.NoError(t, os.MkdirAll(bogusPath, 0o755))

	wt := db.Worktree{
		ID:           "wt-bogus",
		WorktreePath: bogusPath,
		RepoRoot:     repoDir,
	}
	_, cwErr := svc.Queries.CreateWorktree(context.Background(), db.CreateWorktreeParams{
		ID:           wt.ID,
		WorktreePath: wt.WorktreePath,
		RepoRoot:     wt.RepoRoot,
	})
	require.NoError(t, cwErr)

	err := svc.removeWorktreeFromDisk(wt)
	assert.Error(t, err, "git-remove failure with path intact should return error")
	assert.Contains(t, err.Error(), bogusPath)

	// Disk path still present.
	_, statErr := os.Stat(bogusPath)
	assert.NoError(t, statErr)

	// The row OUTLIVES a removal that did not happen. It is the only handle
	// anything keeps on the directory: ListOrphanCandidateWorktrees filters
	// `deleted_at IS NULL`, so a tombstone here strands the directory forever
	// and no later repair brings it back as a GC candidate.
	row, err := svc.Queries.GetWorktreeByID(context.Background(), wt.ID)
	require.NoError(t, err)
	assert.False(t, row.DeletedAt.Valid, "a failed removal must preserve the tracking record")
}

func TestRemoveWorktreeFromDisk_PathMissing_ReturnsNil(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "rwtfd-missing")
	// Never create the directory — it's missing from the start.

	wt := db.Worktree{
		ID:           "wt-missing",
		WorktreePath: wtDir,
		RepoRoot:     repoDir,
	}
	_, cwErr := svc.Queries.CreateWorktree(context.Background(), db.CreateWorktreeParams{
		ID:           wt.ID,
		WorktreePath: wt.WorktreePath,
		RepoRoot:     wt.RepoRoot,
	})
	require.NoError(t, cwErr)

	err := svc.removeWorktreeFromDisk(wt)
	assert.NoError(t, err, "missing path should count as success")

	row, err := svc.Queries.GetWorktreeByID(context.Background(), wt.ID)
	require.NoError(t, err)
	assert.True(t, row.DeletedAt.Valid, "DB row soft-deleted")
}

// TestRemoveWorktreeFromDisk_PathMissingButStillListed_ReturnsError is the
// other half of the pair above, and the one an absent-path check alone got
// wrong.
//
// `git worktree list` reads the admin directory, not the working tree, so git
// keeps a LOCKED worktree in its listing after the user deletes the directory
// by hand -- and keeps refusing to remove it. Reporting "the path is gone" as a
// success there told the user "Worktree removed" over a worktree git still
// held, on a branch that still existed, with the DB row tombstoned so nothing
// revisited it.
func TestRemoveWorktreeFromDisk_PathMissingButStillListed_ReturnsError(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "rwtfd-missing-locked")
	run(t, repoDir, "git", "worktree", "add", "-b", "rwtfd-missing-locked", wtDir)
	run(t, repoDir, "git", "worktree", "lock", "--reason", "held", wtDir)

	wtID, err := svc.ensureTrackedWorktree(context.Background(), wtDir)
	require.NoError(t, err)
	wt, err := svc.Queries.GetWorktreeByID(context.Background(), wtID)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(wtDir))

	err = svc.removeWorktreeFromDisk(wt)
	assert.Error(t, err, "git refused the removal, so the close must not report a removal")
	assert.Contains(t, err.Error(), wt.WorktreePath)

	// The refusal is git's: the worktree and its branch both survive.
	entries, listErr := listGitWorktrees(context.Background(), repoDir)
	require.NoError(t, listErr)
	assert.NotNil(t, worktreeEntryFor(entries, wt.WorktreePath),
		"git still holds the worktree, which is exactly why the failure must be reported")
	assert.True(t, localBranchExists(t, repoDir, "rwtfd-missing-locked"))

	row, err := svc.Queries.GetWorktreeByID(context.Background(), wtID)
	require.NoError(t, err)
	assert.False(t, row.DeletedAt.Valid, "the row must survive a removal git refused")
}

// gitStillListsWorktree answers "still listed" when it cannot read the
// listing, and that direction is deliberate: its caller uses it to SUPPRESS a
// failure report, so a failure the worker cannot disprove must reach the user.
// Real git output never exercises this branch, so nothing else reaches it.
func TestGitStillListsWorktree_UnreadableListingReportsStillListed(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "listing-probe-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "listing-probe", wtDir)

	// The control: a readable listing answers truthfully in both directions.
	assert.True(t, svc.gitStillListsWorktree(context.Background(), db.Worktree{
		RepoRoot: repoDir, WorktreePath: wtDir,
	}))
	assert.False(t, svc.gitStillListsWorktree(context.Background(), db.Worktree{
		RepoRoot: repoDir, WorktreePath: filepath.Join(t.TempDir(), "never-added"),
	}))

	// The fail-safe: a RepoRoot that is not a repository at all.
	assert.True(t, svc.gitStillListsWorktree(context.Background(), db.Worktree{
		RepoRoot: t.TempDir(), WorktreePath: wtDir,
	}), "an unreadable listing must not be read as 'git let it go'")
}

// A locked worktree still permits database-link cleanup. Cleanup never asks Git to remove it.
func TestCleanupOrphanWorktreeLinks_PreservesLockedWorktree(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)
	ctx := context.Background()

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "reap-locked")
	run(t, repoDir, "git", "worktree", "add", "-b", "reap-locked", wtDir)
	run(t, repoDir, "git", "worktree", "lock", "--reason", "held", wtDir)
	wtID, err := svc.ensureTrackedWorktree(ctx, wtDir)
	require.NoError(t, err)
	// A strand link: a worktree_tabs row for a tab that no longer lives. That
	// is what makes it an orphan CANDIDATE rather than a mid-creation worktree.
	svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "reap-strand")

	before, err := svc.Queries.ListOrphanCandidateWorktrees(ctx)
	require.NoError(t, err)
	require.True(t, containsWorktreeID(before, wtID), "the strand link makes it a candidate")

	stored, err := svc.Queries.GetWorktreeByID(ctx, wtID)
	require.NoError(t, err)
	require.NoError(t, svc.CleanupOrphanWorktreeLinks(ctx, stored))

	assert.DirExists(t, wtDir, "cleanup must preserve the locked directory")
	row, err := svc.Queries.GetWorktreeByID(ctx, wtID)
	require.NoError(t, err)
	assert.False(t, row.DeletedAt.Valid)

	after, err := svc.Queries.ListOrphanCandidateWorktrees(ctx)
	require.NoError(t, err)
	assert.False(t, containsWorktreeID(after, wtID), "cleanup removes the dead links without changing the worktree")
}

func containsWorktreeID(rows []db.Worktree, id string) bool {
	for _, row := range rows {
		if row.ID == id {
			return true
		}
	}
	return false
}

// --- Shutdown waiting -------------------------------------------------

func TestShutdown_WaitsForInFlightClose(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	// Intentionally do NOT defer drainAllInFlight — this test invokes
	// Shutdown explicitly, which performs the same wait.

	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		ID:         "agent-slow",
		WorkingDir: t.TempDir(),
	}))

	// Manually Add(1) to the cleanup wait-group to simulate an
	// in-flight close still doing work while Shutdown races it.
	svc.Cleanup.Add(1)

	shutdownDone := make(chan struct{})
	go func() {
		svc.Shutdown()
		close(shutdownDone)
	}()

	// Shutdown must not complete while a handler is still in flight.
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned while close was still in flight")
	case <-time.After(150 * time.Millisecond):
	}

	svc.Cleanup.Done()

	select {
	case <-shutdownDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not return after cleanup finished")
	}

	// Smoke: dispatcher still functional post-shutdown isn't required.
	_ = d
	_ = w
}

func TestShutdown_WaitsForCloseAfterHandlerPanic(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)

	// Simulate a handler that panics: Add(1) has been called, and the
	// deferred Done() MUST still fire.
	func() {
		defer func() { _ = recover() }()
		svc.Cleanup.Add(1)
		defer svc.Cleanup.Done()
		panic("simulated handler panic")
	}()

	done := make(chan struct{})
	go func() {
		svc.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown hung after a handler panic")
	}
}

// Workspace deletion closes its tabs and leaves their dead links for database cleanup.
// Neither operation removes the Git directory or branch.
func TestCleanupWorkspace_PreservesWorktreeAndCleansLinks(t *testing.T) {
	t.Parallel()

	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "cleanup-reclaimable")

	dispatch(fx.d, leapmuxv1connect.WorkerTabServiceCleanupWorkspaceProcedure, &leapmuxv1.CleanupWorkspaceRequest{
		Tabs: []*leapmuxv1.TabIdent{
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: fx.tabID},
		},
	}, fx.w)

	require.Empty(t, fx.w.errors, "unexpected RPC errors: %+v", fx.w.errors)

	count, err := fx.svc.Queries.CountWorktreeTabs(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.Positive(t, count, "the worktree link must survive so the orphan reconciler can still see a candidate")
	live, err := fx.svc.Queries.CountLiveWorktreeRefs(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.Zero(t, live, "the closed tab must leave the link as a strand, not a live reference")

	// And the reconciler must actually be able to finish the job.
	wt, err := fx.svc.Queries.GetWorktreeByID(context.Background(), fx.wtID)
	require.NoError(t, err)
	require.NoError(t, fx.svc.CleanupOrphanWorktreeLinks(context.Background(), wt))
	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr, "workspace cleanup preserves the directory")
}

// TestOrphanReconciler_ClosesAnAgentThroughTheSharedTeardown pins that the
// OFFLINE convergence path performs the same teardown as an online CloseTab.
//
// The reconciler used to run only the DB close plus StopAgent, skipping the
// registered cleanups -- which is the spawnControlIPC teardown, so the tab's
// unix-socket listener stayed open and its delegation token stayed UNREVOKED for
// the life of the worker process. This commit made the offline path the normal
// one for a close, which is what turned that discrepancy into a leak.
//
// The cleanup registry is the observable seam: it is what the remote-IPC
// teardown registers into, so asserting it ran is asserting the socket was
// closed and the token released.
func TestOrphanReconciler_ClosesAnAgentThroughTheSharedTeardown(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)
	ctx := context.Background()

	const agentID = "reconcile-teardown-agent"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		ID:         agentID,
		WorkingDir: t.TempDir(),
	}))
	var cleanupRan atomic.Bool
	svc.agentCleanups.register(agentID, func() { cleanupRan.Store(true) })

	svc.CloseTabForReconcile(leapmuxv1.TabType_TAB_TYPE_AGENT, "", agentID)

	assert.True(t, cleanupRan.Load(),
		"the reconciler must run the tab's registered cleanups -- that is the remote-IPC teardown that closes its socket and revokes its delegation token")
	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid, "the DB row must still be closed")
}

// TestGitIndexLock_ExcludesSameRepoOnly pins the primitive directly: two holders
// of one repo's lock never overlap, and a different repo's lock is still
// acquirable while the first is held.
//
// Tested at the lock rather than through `git worktree add`, because a
// coarser test does not discriminate. Concurrent adds of DIFFERENT branches each
// create their own `.git/worktrees/<name>/index.lock`, so they do not contend at
// all -- a test built on them passes with the lock removed and proves nothing.
// What the lock actually buys is mutual exclusion between commands that DO share
// an index (add vs. checkout vs. commit), and that is this property.
func TestGitIndexLock_ExcludesSameRepoOnly(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)

	var inside atomic.Int32
	var maxObserved atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = svc.withGitIndexLock("/repo-a", func() error {
				n := inside.Add(1)
				for {
					prev := maxObserved.Load()
					if n <= prev || maxObserved.CompareAndSwap(prev, n) {
						break
					}
				}
				// Widen the window a real git fork would occupy, so an unlocked
				// implementation is overwhelmingly likely to be caught.
				time.Sleep(2 * time.Millisecond)
				inside.Add(-1)
				return nil
			})
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int32(1), maxObserved.Load(),
		"two holders of the same repo's index lock overlapped, so git commands in that repo can still collide on index.lock")

	// Different repos must NOT contend: over-serializing every repository behind
	// one lock would make an unrelated agent's startup wait on this one.
	held := svc.gitIndexLock("/repo-a")
	held.Lock()
	other := make(chan struct{})
	go func() {
		defer close(other)
		_ = svc.withGitIndexLock("/repo-b", func() error { return nil })
	}()
	select {
	case <-other:
	case <-time.After(2 * time.Second):
		t.Fatal("a different repository's index lock blocked behind /repo-a's; the lock is not per-repo")
	}
	held.Unlock()
}

// TestExecuteCreateWorktree_RecoversFromOrphanedAdminDir is the regression for a
// PERMANENT failure, and the reason `worktree add` prunes first.
//
// `git worktree add` creates the admin dir (.git/worktrees/<name>/, index and
// lock included) BEFORE the worktree directory. A ctx cancellation in that window
// -- an agent closed during startup, a dropped channel -- leaves the admin dir
// with no worktree beside it. validateCreateWorktree cannot see that: it stats the
// WORKTREE path, which does not exist. So every later add for that branch name
// died with `Unable to create '.git/worktrees/<name>/index.lock': File exists`,
// forever, until someone pruned by hand.
//
// The orphan is built the way it really arises -- a real add, then the worktree
// directory removed -- rather than by hand-crafting files, so the test exercises
// the state git itself leaves behind. `--detach` keeps the branch name free, which
// is what lets the request past the validator's "branch already exists" check and
// down to the failing add.
func TestExecuteCreateWorktree_RecoversFromOrphanedAdminDir(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)
	repoDir := initRepo(t)
	ctx := context.Background()

	const branch = "orphan-admin-recover"
	orphanPath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-worktrees", branch)
	run(t, repoDir, "git", "worktree", "add", "--detach", orphanPath)
	require.NoError(t, os.RemoveAll(orphanPath), "simulate the cancelled add: the worktree dir goes, the admin dir stays")
	adminDir := filepath.Join(repoDir, ".git", "worktrees", branch)
	require.DirExists(t, adminDir, "the orphaned admin dir is the precondition under test")

	plan, err := svc.validateGitMode(ctx, repoDir, createWorktreeSetup(branch, ""))
	require.NoError(t, err, "the validator cannot see the orphan, so it must admit this request")

	result, err := svc.executeGitMode(ctx, plan)

	require.NoError(t, err, "an orphaned admin dir must not permanently block the branch name")
	assert.DirExists(t, orphanPath, "the worktree must actually be created")
	assert.NotEmpty(t, result.WorktreeID, "and tracked, so a later REMOVE close can reach it")
}

// TestExecuteCreateWorktree_RecoversFromInterruptedAddLock extends the
// orphaned-admin-dir case with the file a real interrupted add leaves that a
// completed one does not: the admin dir's index.lock. That is the file the next
// `git worktree add` refuses to create:
//
//	fatal: Unable to create '.git/worktrees/<name>/index.lock': File exists.
//	Another git process seems to be running in this repository, or the lock
//	file may be stale
//
// The prune already handles this shape -- the entry's gitdir points at a
// directory that no longer exists, so prune reclaims the whole admin dir, lock
// and all, at any expiry. This test pins that, because the shape is one step
// closer to what production hit than the sibling test's, and a future change to
// the prune (dropping it, or narrowing it with an --expire) would silently
// re-open a PERMANENT failure for that branch name.
//
// It does NOT cover the harder shape seen once under load, where the worktree
// directory survives the interruption and prune therefore keeps the entry --
// see the note on the prune call in executeCreateWorktree.
func TestExecuteCreateWorktree_RecoversFromInterruptedAddLock(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)
	repoDir := initRepo(t)
	ctx := context.Background()

	const branch = "interrupted-add-lock"
	orphanPath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-worktrees", branch)
	run(t, repoDir, "git", "worktree", "add", "--detach", orphanPath)
	require.NoError(t, os.RemoveAll(orphanPath), "the worktree dir goes, the admin dir stays")
	adminDir := filepath.Join(repoDir, ".git", "worktrees", branch)
	require.DirExists(t, adminDir)
	// The part an interrupted add leaves behind and a completed one does not.
	require.NoError(t, os.WriteFile(filepath.Join(adminDir, "index.lock"), nil, 0o644))

	plan, err := svc.validateGitMode(ctx, repoDir, createWorktreeSetup(branch, ""))
	require.NoError(t, err, "the validator stats the worktree path, which is gone, so it admits this request")

	result, err := svc.executeGitMode(ctx, plan)

	require.NoError(t, err, "a stale index.lock must not permanently block the branch name")
	assert.DirExists(t, orphanPath, "the worktree must actually be created")
	assert.NotEmpty(t, result.WorktreeID, "and tracked, so a later REMOVE close can reach it")
}

// An already closed tab cannot authorize deletion, including deletion of uncommitted work.
func TestCloseTabCommon_RemoveOnAlreadyClosedTabDoesNotDiscardUnsavedWork(t *testing.T) {
	t.Parallel()

	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "stale-remove")
	ctx := context.Background()

	// Uncommitted work in the worktree -- what the user would have been warned
	// about had the dialog reached them.
	wt, err := fx.svc.Queries.GetWorktreeByID(ctx, fx.wtID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(wt.WorktreePath, "dirty.txt"), []byte("unsaved\n"), 0o644))

	// The row is ALREADY closed, so the REMOVE below retires nothing live --
	// exactly what a stale client produces.
	require.NoError(t, closeErr(fx.svc.Queries.CloseAgent(ctx, fx.tabID)))

	result := fx.svc.closeAgentTabCommon("", fx.tabID,
		leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)

	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED,
		result.GetWorktreeRemoval(),
		"a REMOVE that retired no live row must not silently report the directory gone")
	assert.Equal(t, "Worktree kept: deletion was not authorized", result.GetFailureMessage(),
		"this refusal is about unconfirmed work, unlike the git-refusal copy")
	assert.DirExists(t, wt.WorktreePath,
		"the worktree holds uncommitted work and nobody confirmed its deletion, so it must survive")

	row, err := fx.svc.Queries.GetWorktreeByID(ctx, fx.wtID)
	require.NoError(t, err)
	assert.False(t, row.DeletedAt.Valid, "the row must not be soft-deleted when the removal was refused")
}

// TestCloseTabCommon_RemoveOnLiveTabStillHonoursTheUsersDelete is the other half:
// when the close DOES retire a live row, the user saw the last-tab dialog with the
// dirty and unpushed counts and chose Delete anyway. Second-guessing that would
// make the button lie, so the probe must not run.
func TestCloseTabCommon_RemoveOnLiveTabStillHonoursTheUsersDelete(t *testing.T) {
	t.Parallel()

	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "confirmed-remove")
	ctx := context.Background()

	wt, err := fx.svc.Queries.GetWorktreeByID(ctx, fx.wtID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(wt.WorktreePath, "dirty.txt"), []byte("unsaved\n"), 0o644))

	// Row is LIVE, so this close retires it: a user-confirmed delete.
	result := fx.svc.closeAgentTabCommon("", fx.tabID,
		leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)

	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED,
		result.GetWorktreeRemoval(),
		"a confirmed delete of a live tab must still remove the worktree even when dirty")
	assert.NoDirExists(t, wt.WorktreePath, "the user asked for the directory to go")
}

func TestExistingCheckoutPreservationUsesCheckoutBytes(t *testing.T) {
	for _, tc := range []struct {
		name, autocrlf, eol, contents string
	}{
		{"lf", "false", "lf", "preserve me\n"},
		{"crlf", "true", "crlf", "preserve me\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Environment configuration reaches the fixture's first checkout on every platform.
			t.Setenv("GIT_CONFIG_COUNT", "2")
			t.Setenv("GIT_CONFIG_KEY_0", "core.autocrlf")
			t.Setenv("GIT_CONFIG_VALUE_0", tc.autocrlf)
			t.Setenv("GIT_CONFIG_KEY_1", "core.eol")
			t.Setenv("GIT_CONFIG_VALUE_1", tc.eol)
			checkout := existingCheckoutForSafetyTest(t)
			contents, err := os.ReadFile(filepath.Join(checkout.nested, "committed.txt"))
			require.NoError(t, err)
			require.Equal(t, tc.contents, string(contents), "the fixture must exercise the selected checkout format")
			checkout.assertPreserved(t)
		})
	}
}

type checkoutSafetyFixture struct {
	repo, worktree, nested string
	contents               []byte
}

// existingCheckoutForSafetyTest captures the bytes that Git actually checks out.
// Host Git settings can change line endings between the commit and the checkout.
func existingCheckoutForSafetyTest(t *testing.T) checkoutSafetyFixture {
	t.Helper()
	repo := initRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "frontend", "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "frontend", "src", "committed.txt"), []byte("preserve me\n"), 0o644))
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-m", "Add committed contents")
	wt := filepath.Join(t.TempDir(), "checkout")
	run(t, repo, "git", "worktree", "add", "-b", "safety-branch", wt)
	nested := filepath.Join(wt, "frontend", "src")
	contents, err := os.ReadFile(filepath.Join(nested, "committed.txt"))
	require.NoError(t, err)
	return checkoutSafetyFixture{repo: repo, worktree: wt, nested: nested, contents: contents}
}

func (checkout checkoutSafetyFixture) assertPreserved(t *testing.T) {
	t.Helper()
	assert.DirExists(t, checkout.worktree)
	entries, err := listGitWorktrees(context.Background(), checkout.repo)
	require.NoError(t, err)
	// Git prints forward slashes on Windows. Compare parsed paths through the shared path helper.
	entry := worktreeEntryFor(entries, checkout.worktree)
	require.NotNil(t, entry, "Git must retain the worktree registration")
	assert.False(t, entry.GetLocked(), "preservation must not depend on a Git worktree lock")
	assert.Equal(t, "safety-branch", entry.GetBranch())
	_, err = gitutil.Output(context.Background(), checkout.repo, "rev-parse", "--verify", "refs/heads/safety-branch")
	assert.NoError(t, err)
	contents, err := os.ReadFile(filepath.Join(checkout.nested, "committed.txt"))
	require.NoError(t, err)
	assert.Equal(t, checkout.contents, contents, "the checkout must retain its original bytes")
}

func TestNestedForcedRemovePreservesExistingCheckout(t *testing.T) {
	t.Parallel()
	for _, tabType := range []leapmuxv1.TabType{
		leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL,
		leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE,
	} {
		t.Run(tabType.String(), func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			checkout := existingCheckoutForSafetyTest(t)
			wt, nested := checkout.worktree, checkout.nested
			wtID, err := svc.ensureTrackedWorktree(context.Background(), wt)
			require.NoError(t, err)
			const tabID = "nested"
			const userID = "user-1"
			var result *leapmuxv1.CloseTabResponse
			switch tabType {
			case leapmuxv1.TabType_TAB_TYPE_AGENT:
				createAgentForPath(t, svc, tabID, nested)
				svc.registerTabForWorktree(wtID, tabType, tabID)
				result = svc.closeAgentTabCommon(userID, tabID, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
			case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
				createTerminalForPath(t, svc, tabID, nested)
				svc.registerTabForWorktree(wtID, tabType, tabID)
				result = svc.closeTerminalTabCommon(userID, tabID, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
			case leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE:
				payload := fileTabPayload(filepath.Join(nested, "committed.txt"), nested)
				if tabType == leapmuxv1.TabType_TAB_TYPE_IMAGE {
					payload = &leapmuxv1.TabPayload{WorkingDir: nested, Kind: &leapmuxv1.TabPayload_Image{Image: &leapmuxv1.ImageTabPayload{AgentId: "source", Seq: 1}}}
				}
				require.NoError(t, svc.TabPayloads.Register(context.Background(), RegisterTabPayloadParams{UserID: userID, TabID: tabID, Payload: payload}))
				result = svc.closePayloadTabCommon(userID, tabID, tabType, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
			default:
				t.Fatal("unexpected tab type")
			}
			assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, result.WorktreeRemoval)
			checkout.assertPreserved(t)
		})
	}
}

func TestUnconfirmedStaleRemovePreservesCleanExistingCheckout(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	checkout := existingCheckoutForSafetyTest(t)
	wt := checkout.worktree
	wtID, err := svc.ensureTrackedWorktree(context.Background(), wt)
	require.NoError(t, err)
	createAgentForPath(t, svc, "closed", wt)
	svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "closed")
	require.NoError(t, closeErr(svc.Queries.CloseAgent(context.Background(), "closed")))
	result := svc.closeAgentTabCommon("user-1", "closed", leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, result.WorktreeRemoval)
	checkout.assertPreserved(t)
}

func TestCloseTabWithoutWorkingDirDoesNotInspectWorkerCheckout(t *testing.T) {
	svc, _, _ := setupTestService(t)
	checkout := existingCheckoutForSafetyTest(t)
	t.Chdir(checkout.worktree)
	createAgentForPath(t, svc, "without-directory", "")
	result := svc.closeAgentTabCommon("user-1", "without-directory", leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
	assert.Empty(t, result.WorktreePath, "a missing tab directory must not select the Worker process directory")
	checkout.assertPreserved(t)
}

// --- CloseTab request validation ---------------------------------------------

// CloseTabRequest carries the tab's type, which each of the three close RPCs it
// replaced implied. A request that states no usable identity has no close to
// run, so it is refused before any row is read, and the agent it names stays
// open.
func TestCloseTab_RefusesAMalformedTab(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		tab  *leapmuxv1.TabIdent
	}{
		{"absent tab", nil},
		{"empty tab id", &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}},
		{"unspecified type", &leapmuxv1.TabIdent{TabId: "agent-1"}},
		{"unknown type", &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType(99), TabId: "agent-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, d, w := setupTestService(t)
			seedAgent(t, svc, "agent-1")

			dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: tc.tab}, w)

			require.Len(t, w.errors, 1)
			assert.Equal(t, codeInvalidArgument, w.errors[0].code)
			assert.Empty(t, w.responses)
			row, err := svc.Queries.GetAgentByID(context.Background(), "agent-1")
			require.NoError(t, err)
			assert.False(t, row.ClosedAt.Valid, "a refused close must not stamp closed_at")
		})
	}
}

// A payload-backed close whose row is already gone still drops the worktree
// link, and it drops the link of the kind the caller stated: the link row
// carries the tab type, so a guessed FILE would leave an IMAGE link standing.
func TestCloseTab_AnAbsentPayloadRowDropsTheLinkOfTheStatedKind(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)
	ctx := context.Background()
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "image-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "image-wt", wtDir)
	wtID, err := svc.ensureTrackedWorktree(ctx, wtDir)
	require.NoError(t, err)
	require.NoError(t, svc.Queries.AddWorktreeTab(ctx, db.AddWorktreeTabParams{
		WorktreeID: wtID, TabType: leapmuxv1.TabType_TAB_TYPE_IMAGE, TabID: "image-1", UserID: "user-1",
	}))

	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab:            &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_IMAGE, TabId: "image-1"},
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	_, err = svc.Queries.GetWorktreeForTab(ctx, db.GetWorktreeForTabParams{
		TabType: leapmuxv1.TabType_TAB_TYPE_IMAGE, TabID: "image-1", UserID: "user-1",
	})
	require.ErrorIs(t, err, sql.ErrNoRows, "the IMAGE link must go with the close")
	assert.DirExists(t, wtDir, "a KEEP close leaves the worktree on disk")
}

// A payload-backed close reads the stored kind and ignores the kind the caller
// stated. The link row carries the tab type, so a close of an IMAGE tab that
// the caller calls a FILE tab must still drop the IMAGE link.
func TestCloseTab_TheStoredPayloadKindDecidesWhichLinkToDrop(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)
	ctx := context.Background()
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "stored-kind-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "stored-kind-wt", wtDir)
	_, err := svc.ensureTrackedWorktree(ctx, wtDir)
	require.NoError(t, err)
	// Register links the tab to the tracked worktree as an IMAGE tab.
	require.NoError(t, svc.TabPayloads.Register(ctx, RegisterTabPayloadParams{
		UserID: "user-1", TabID: "image-1",
		Payload: &leapmuxv1.TabPayload{WorkingDir: wtDir, Kind: &leapmuxv1.TabPayload_Image{Image: &leapmuxv1.ImageTabPayload{AgentId: "source", Seq: 1}}},
	}))
	_, err = svc.Queries.GetWorktreeForTab(ctx, db.GetWorktreeForTabParams{
		TabType: leapmuxv1.TabType_TAB_TYPE_IMAGE, TabID: "image-1", UserID: "user-1",
	})
	require.NoError(t, err, "precondition: the IMAGE tab is linked to its worktree")

	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab:            fileTabIdent("image-1"),
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	_, err = svc.TabPayloads.TabTypeOf(ctx, "user-1", "image-1")
	require.ErrorIs(t, err, ErrTabPayloadNotFound, "the close revokes the payload row")
	_, err = svc.Queries.GetWorktreeForTab(ctx, db.GetWorktreeForTabParams{
		TabType: leapmuxv1.TabType_TAB_TYPE_IMAGE, TabID: "image-1", UserID: "user-1",
	})
	require.ErrorIs(t, err, sql.ErrNoRows, "the stored IMAGE kind must decide which link the close drops")
	assert.DirExists(t, wtDir, "a KEEP close leaves the worktree on disk")
}

// Only an absent payload row may fall back to the stated kind. A read that
// fails says nothing about the kind, so the close is refused before it changes
// anything: the guess could miss the link, and a requested REMOVE would then
// turn into a KEEP that nobody reports.
func TestCloseTab_APayloadReadFailureRefusesTheClose(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)
	ctx := context.Background()
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "read-failure-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "read-failure-wt", wtDir)
	wtID, err := svc.ensureTrackedWorktree(ctx, wtDir)
	require.NoError(t, err)
	require.NoError(t, svc.Queries.AddWorktreeTab(ctx, db.AddWorktreeTabParams{
		WorktreeID: wtID, TabType: leapmuxv1.TabType_TAB_TYPE_IMAGE, TabID: "image-1", UserID: "user-1",
	}))

	// A renamed table makes the kind read fail with a store error, not with
	// ErrTabPayloadNotFound. SQLite rewrites the views that refer to the table,
	// so the rest of the schema keeps working.
	_, err = svc.DB.ExecContext(ctx, "ALTER TABLE worker_tab_payloads RENAME TO worker_tab_payloads_hidden")
	require.NoError(t, err)
	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab:            &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_IMAGE, TabId: "image-1"},
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	}, w)
	_, err = svc.DB.ExecContext(ctx, "ALTER TABLE worker_tab_payloads_hidden RENAME TO worker_tab_payloads")
	require.NoError(t, err)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.Internal), w.errors[0].code)
	assert.Empty(t, w.responses)
	_, err = svc.Queries.GetWorktreeForTab(ctx, db.GetWorktreeForTabParams{
		TabType: leapmuxv1.TabType_TAB_TYPE_IMAGE, TabID: "image-1", UserID: "user-1",
	})
	require.NoError(t, err, "a refused close keeps the worktree link")
	assert.DirExists(t, wtDir, "a refused close removes no worktree")
}
