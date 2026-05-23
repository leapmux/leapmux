package service

import (
	"database/sql"
	"errors"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// closeFileTabCommon drives the shared closeTabCommon flow for FILE
// tabs. It exists so the FILE close path uses the same worktree-tab
// link drop and conditional `git worktree remove` machinery as
// CloseAgent / CloseTerminal — the only file-tab specific work is
// dropping the worker_file_tab row (which doubles as the
// FileTabPathRevoked emit). stopProcess is a noop because file tabs
// own no process on the worker.
//
// Used by the RevokeFileTabPath RPC and by the orphan reconciler. The
// orphan reconciler passes WorktreeAction_KEEP because the hub's
// "this tab is gone" signal says nothing about the worktree.
func (svc *Context) closeFileTabCommon(orgID, tabID string, action leapmuxv1.WorktreeAction) *leapmuxv1.CloseTabResult {
	return svc.closeTabCommon(
		leapmuxv1.TabType_TAB_TYPE_FILE,
		tabID,
		action,
		func() {},
		func() error {
			err := svc.FileTabPaths.RevokeRow(bgCtx(), orgID, tabID)
			// Idempotent: the row may have been deleted by a concurrent
			// close. closeTabCommon proceeds to drop the worktree link
			// regardless.
			if errors.Is(err, ErrFileTabPathNotFound) {
				return nil
			}
			return err
		},
	)
}

// closeTabCommon runs the shared tab-close flow for the CloseAgent /
// CloseTerminal / RevokeFileTabPath handlers AND the orphan
// reconciler. The RPC handlers are registered as tracked dispatcher
// methods (RegisterTracked) so the dispatcher's bound Cleanup
// WaitGroup is Add(1)'d synchronously BEFORE the dispatched goroutine
// launches — Shutdown.Wait can't slip past an in-flight close. The
// orphan reconciler runs in its own background goroutine and tracks
// its own work; closeTabCommon itself stays free of Cleanup.Add to
// avoid the inside-goroutine-Add race the dispatcher-level tracking
// was introduced to fix.
//
// On WorktreeAction_REMOVE, the worktree is resolved BEFORE the
// tab→worktree association is dropped — otherwise we'd lose the link
// needed to decide whether the worktree can be deleted. If a partial
// failure occurs (DB soft-delete, worktree remove) the returned
// result populates failure_message / failure_detail / worktree_path /
// worktree_id so the UI can toast a warning. The returned
// *CloseTabResult is never nil.
func (svc *Context) closeTabCommon(
	tabType leapmuxv1.TabType,
	tabID string,
	action leapmuxv1.WorktreeAction,
	stopProcess func(),
	closeDB func() error,
) *leapmuxv1.CloseTabResult {
	stopProcess()

	// When REMOVE is requested, look up the worktree BEFORE the
	// tab-worktree association is dropped, so we still see the link.
	// The actual removal is gated on CountWorktreeTabs == 0 after
	// unregister, which protects sibling tabs sharing the worktree.
	var wtForRemoval *db.Worktree
	if action == leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE {
		wt, err := svc.Queries.GetWorktreeForTab(bgCtx(), db.GetWorktreeForTabParams{
			TabType: tabType,
			TabID:   tabID,
		})
		switch {
		case err == nil:
			wtForRemoval = &wt
		case errors.Is(err, sql.ErrNoRows):
			// Tab has no worktree association — REMOVE degrades to KEEP.
		default:
			// A real DB error degrades REMOVE to KEEP; log it so the
			// degradation is observable rather than silent.
			slog.Warn("failed to look up worktree for tab close", "tab_type", tabType, "tab_id", tabID, "error", err)
		}
	}

	result := &leapmuxv1.CloseTabResult{}
	if err := closeDB(); err != nil {
		slog.Error("failed to close tab in DB", "tab_type", tabType, "tab_id", tabID, "error", err)
		result.FailureMessage = dbCloseFailureMessage(tabType)
		result.FailureDetail = err.Error()
		return result
	}

	// When we resolved a worktree for REMOVE, guard the association
	// delete by worktree_id via removeTabFromWorktree. Otherwise (KEEP,
	// or REMOVE that degraded to no-worktree) fall through to the cheap
	// single-query unregisterTab.
	if wtForRemoval == nil {
		svc.unregisterTab(tabType, tabID)
		return result
	}

	svc.removeTabFromWorktree(tabType, tabID, wtForRemoval.ID)
	remaining, countErr := svc.Queries.CountWorktreeTabs(bgCtx(), wtForRemoval.ID)
	if countErr != nil {
		slog.Warn("failed to count worktree tabs after close", "worktree_id", wtForRemoval.ID, "error", countErr)
		return result
	}
	if remaining != 0 {
		return result
	}
	if err := svc.removeWorktreeFromDisk(*wtForRemoval, true); err != nil {
		result.FailureMessage = "Failed to remove worktree"
		result.FailureDetail = err.Error()
		result.WorktreePath = wtForRemoval.WorktreePath
		result.WorktreeId = wtForRemoval.ID
	}
	return result
}

func dbCloseFailureMessage(tabType leapmuxv1.TabType) string {
	switch tabType {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		return "Failed to close agent"
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		return "Failed to close terminal"
	case leapmuxv1.TabType_TAB_TYPE_FILE:
		return "Failed to close file"
	default:
		return "Failed to close tab"
	}
}
