package service

import (
	"database/sql"
	"errors"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// closeTabCommon runs the shared tab-close flow for the CloseAgent and
// CloseTerminal handlers. The handler is tracked on svc.Cleanup so a
// concurrent Shutdown drains it. On WorktreeAction_REMOVE, the worktree
// is resolved BEFORE the tab→worktree association is dropped — otherwise
// we'd lose the link needed to decide whether the worktree can be
// deleted. If a partial failure occurs (DB soft-delete, worktree remove)
// the returned result populates failure_message / failure_detail /
// worktree_path / worktree_id so the UI can toast a warning. The
// returned *CloseTabResult is never nil.
func (svc *Context) closeTabCommon(
	tabType leapmuxv1.TabType,
	tabID string,
	action leapmuxv1.WorktreeAction,
	stopProcess func(),
	closeDB func() error,
) *leapmuxv1.CloseTabResult {
	svc.Cleanup.Add(1)
	defer svc.Cleanup.Done()

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
	if remaining == 0 {
		if err := svc.removeWorktreeFromDisk(*wtForRemoval, true); err != nil {
			result.FailureMessage = "Failed to remove worktree"
			result.FailureDetail = err.Error()
			result.WorktreePath = wtForRemoval.WorktreePath
			result.WorktreeId = wtForRemoval.ID
		}
	}
	return result
}

func dbCloseFailureMessage(tabType leapmuxv1.TabType) string {
	switch tabType {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		return "Failed to close agent"
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		return "Failed to close terminal"
	default:
		return "Failed to close tab"
	}
}
