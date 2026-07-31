package service

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/pathutil"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// TestWorktreeTabs_FileLinksAreOwnerScoped pins the (worktree_id, tab_type,
// tab_id, user_id) identity of a worktree_tabs row.
//
// FILE tab ids are minted client-side as file-<epoch-ms>-<per-module-load
// counter>, so two users can produce the same id. With user_id outside the
// primary key the second link is swallowed by AddWorktreeTab's ON CONFLICT DO
// NOTHING, and either user's close then deletes the single surviving row --
// detaching the OTHER user's still-open file tab and letting the worktree GC
// reclaim a directory that is still mounted in an editor.
func TestWorktreeTabs_FileLinksAreOwnerScoped(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	svc.FileTabPaths = NewFileTabPathStore(svc.Queries, nil)
	ctx := context.Background()

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "shared-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "shared-branch", wtDir)
	openPath := filepath.Join(wtDir, "open.txt")
	require.NoError(t, os.WriteFile(openPath, []byte("open\n"), 0o644))

	wtID, err := svc.ensureTrackedWorktree(ctx, wtDir)
	require.NoError(t, err)

	// Two users open the same file under the same worktree and their
	// clients happened to mint the same tab id.
	const sharedTabID = "file-1700000000000-1"
	for _, userID := range []string{"user-a", "user-b"} {
		require.NoError(t, svc.FileTabPaths.Register(ctx, RegisterFileTabPathParams{
			UserID:     userID,
			TabID:      sharedTabID,
			FilePath:   openPath,
			WorkingDir: wtDir,
		}))
	}

	count, err := svc.Queries.CountWorktreeTabs(ctx, wtID)
	require.NoError(t, err)
	require.Equal(t, int64(2), count, "each owner's FILE link must be its own worktree_tabs row")

	// user-b closes its tab. KEEP so the assertion is about the link, not
	// the worktree-removal branch.
	result := svc.closeFileTabCommon("user-b", sharedTabID, leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP, dropWorktreeLink)
	require.Equal(t, "", result.GetFailureMessage(), "FILE close must not report a failure")

	count, err = svc.Queries.CountWorktreeTabs(ctx, wtID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "closing one owner's file tab must leave the other owner's link intact")

	// And the survivor is user-a's, resolvable by its own owner only.
	wt, err := svc.Queries.GetWorktreeForTab(ctx, db.GetWorktreeForTabParams{
		TabType: leapmuxv1.TabType_TAB_TYPE_FILE,
		TabID:   sharedTabID,
		UserID:  "user-a",
	})
	require.NoError(t, err, "user-a's link must still resolve its worktree")
	assert.Equal(t, wtID, wt.ID)

	_, err = svc.Queries.GetWorktreeForTab(ctx, db.GetWorktreeForTabParams{
		TabType: leapmuxv1.TabType_TAB_TYPE_FILE,
		TabID:   sharedTabID,
		UserID:  "user-b",
	})
	assert.True(t, errors.Is(err, sql.ErrNoRows), "user-b's link must be gone (err=%v)", err)

	// user-a's file_tab row is untouched too.
	loc, err := svc.FileTabPaths.Get(ctx, "user-a", sharedTabID)
	require.NoError(t, err)
	assert.Equal(t, openPath, loc.FilePath)
}

// TestWorktreeTabUserID pins the one rule every by-tab worktree_tabs read and
// delete shares: FILE links carry the owner, AGENT/TERMINAL links carry "".
// Callers may pass the authenticated user for any type; only FILE keeps it.
func TestWorktreeTabUserID(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "user-a", worktreeTabUserID(leapmuxv1.TabType_TAB_TYPE_FILE, "user-a"))
	assert.Equal(t, "", worktreeTabUserID(leapmuxv1.TabType_TAB_TYPE_FILE, ""))
	assert.Equal(t, "", worktreeTabUserID(leapmuxv1.TabType_TAB_TYPE_AGENT, "user-a"))
	assert.Equal(t, "", worktreeTabUserID(leapmuxv1.TabType_TAB_TYPE_TERMINAL, "user-a"))
	assert.Equal(t, "", worktreeTabUserID(leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, "user-a"))
}

// TestUnregisterTab_AgentLinkIsOwnerBlind guards the normalization: an AGENT
// close that passes the authenticated user must still match the link
// registerTabForWorktree wrote with user_id "". Without worktreeTabUserID this
// silently deletes nothing and strands the worktree.
func TestUnregisterTab_AgentLinkIsOwnerBlind(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	ctx := context.Background()

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "agent-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "agent-branch", wtDir)
	wtID, err := svc.ensureTrackedWorktree(ctx, wtDir)
	require.NoError(t, err)
	svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1")

	count, err := svc.Queries.CountWorktreeTabs(ctx, wtID)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)

	svc.unregisterTab(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1", "user-a")

	count, err = svc.Queries.CountWorktreeTabs(ctx, wtID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "an AGENT link must drop regardless of the caller's user id")
}

// TestEnsureTrackedWorktree_ConcurrentAdoptionSharesOneRow pins that two
// callers adopting the SAME worktree at once both succeed and agree on the id.
//
// This is not a hypothetical: "use existing worktree" opens a second agent on a
// worktree that already has one, and each agent adopts on its own async startup
// goroutine. ensureTrackedWorktree looks the row up and then inserts it, so both
// callers could miss and both insert. idx_worktrees_path is UNIQUE over live
// rows, so before CreateWorktree became conflict-tolerant the loser surfaced a
// raw `UNIQUE constraint failed: worktrees.worktree_path` out of agent startup.
//
// The second half matters as much as the first: tolerating the conflict without
// reading the row back would let the loser return an id nothing stored, and
// every ref-count and close keyed on that id would silently miss.
func TestEnsureTrackedWorktree_ConcurrentAdoptionSharesOneRow(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	ctx := context.Background()

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "race-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "race-branch", wtDir)

	const callers = 8
	ids := make([]string, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them together so they collide on the miss
			ids[i], errs[i] = svc.ensureTrackedWorktree(ctx, wtDir)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "caller %d must not surface the insert conflict", i)
	}
	for i, got := range ids {
		assert.Equal(t, ids[0], got, "caller %d returned a different worktree id", i)
	}

	// The returned id must be the one actually stored, not merely agreed on.
	stored, err := svc.Queries.GetWorktreeByPath(ctx, pathutil.Canonicalize(wtDir))
	require.NoError(t, err)
	assert.Equal(t, stored.ID, ids[0], "the id every caller got must be the live row's")
}

// TestCreateWorktree_ConflictReturnsTheWinningRow pins the query's contract now
// that it carries one: the loser of a concurrent insert gets back the row that
// actually exists, not the id it minted.
//
// This used to be prose on the SQL ("Callers must read the row back rather than
// assume their id won") enforced only by the single caller remembering a second
// SELECT. A caller that skipped it would hand back an id no row carries, and
// every later ref-count and close keyed on that id would silently miss.
func TestCreateWorktree_ConflictReturnsTheWinningRow(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	q := svc.Queries
	ctx := context.Background()

	winner, err := q.CreateWorktree(ctx, db.CreateWorktreeParams{
		ID: "wt-winner", WorktreePath: "/r/shared", RepoRoot: "/r", BranchName: "b",
	})
	require.NoError(t, err)
	require.Equal(t, "wt-winner", winner.ID)

	loser, err := q.CreateWorktree(ctx, db.CreateWorktreeParams{
		ID: "wt-loser", WorktreePath: "/r/shared", RepoRoot: "/r", BranchName: "b",
	})
	require.NoError(t, err, "a concurrent insert on the same path must not surface a constraint error")
	assert.Equal(t, "wt-winner", loser.ID, "the loser must be handed the id that is actually stored")
	assert.Equal(t, "/r/shared", loser.WorktreePath)

	// The conflict target is idx_worktrees_path, so a PRIMARY KEY collision is
	// still raised rather than swallowed by an untargeted DO NOTHING.
	_, err = q.CreateWorktree(ctx, db.CreateWorktreeParams{
		ID: "wt-winner", WorktreePath: "/r/other", RepoRoot: "/r", BranchName: "b",
	})
	assert.Error(t, err, "an id collision is a real bug and must not be swallowed")
}
