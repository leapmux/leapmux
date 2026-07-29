package service

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
			UserID:   userID,
			TabID:    sharedTabID,
			FilePath: openPath,
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
	path, err := svc.FileTabPaths.Get(ctx, "user-a", sharedTabID)
	require.NoError(t, err)
	assert.Equal(t, openPath, path)
}

// TestWorktreeTabUserID pins the one rule every by-tab worktree_tabs read and
// delete shares: FILE links carry the owner, AGENT/TERMINAL links carry "".
// Callers may pass the authenticated user for any type; only FILE keeps it.
func TestWorktreeTabUserID(t *testing.T) {
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
