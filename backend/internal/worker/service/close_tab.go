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
// Used only by the RevokeFileTabPath RPC. The orphan reconciler does
// NOT route through here: reconcileFileTabs drops the worktree_tabs
// link and revokes the file_tab row directly, precisely to avoid the
// worktree-removal branch this flow owns (the hub's "this tab is gone"
// signal says nothing about whether the worktree should be removed).
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
// CloseTerminal / RevokeFileTabPath handlers. The handlers are
// registered as tracked dispatcher methods (RegisterTracked) so the
// dispatcher's bound Cleanup WaitGroup is Add(1)'d synchronously
// BEFORE the dispatched goroutine launches — Shutdown.Wait can't slip
// past an in-flight close. closeTabCommon itself stays free of
// Cleanup.Add to avoid the inside-goroutine-Add race the
// dispatcher-level tracking was introduced to fix. The orphan
// reconciler does NOT route through here (see closeFileTabCommon): it
// drops the worktree link directly so it never takes the
// worktree-removal branch below.
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

	result := &leapmuxv1.CloseTabResult{}

	// When REMOVE is requested, look up the worktree BEFORE the
	// tab-worktree association is dropped, so we still see the link.
	// The actual removal is gated on CountWorktreeTabs == 0 after
	// unregister, which protects sibling tabs sharing the worktree.
	var wtForRemoval *db.Worktree
	worktreeLookupFailed := false
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
			// Leaves worktree_removal UNSPECIFIED.
		default:
			// A real DB error means we can't tell whether this close
			// should remove the worktree. Surface it as a partial failure
			// (rather than silently degrading to KEEP) so the caller can
			// warn the user that the worktree may need manual cleanup.
			slog.Warn("failed to look up worktree for tab close", "tab_type", tabType, "tab_id", tabID, "error", err)
			result.FailureMessage = "Failed to check worktree for removal"
			result.FailureDetail = err.Error()
			result.WorktreeRemoval = leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED
			worktreeLookupFailed = true
		}
	}

	if err := closeDB(); err != nil {
		slog.Error("failed to close tab in DB", "tab_type", tabType, "tab_id", tabID, "error", err)
		result.FailureMessage = dbCloseFailureMessage(tabType)
		result.FailureDetail = err.Error()
		// The tab didn't close, so no worktree work ran below. Own the
		// worktree-removal outcome here so the result is coherent: for a
		// REMOVE, the removal the user asked for failed (this also
		// overwrites the FAILED-but-"couldn't look up worktree" partial
		// state the lookup-error branch above may have left, which would
		// otherwise pair this close-failure message with a stale outcome);
		// for KEEP it stays UNSPECIFIED. No worktree path is attached —
		// the worktree itself was never touched, so there is nothing for
		// the user to clean up by hand.
		if action == leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE {
			result.WorktreeRemoval = leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED
		}
		return result
	}

	// When we resolved a worktree for REMOVE, guard the association
	// delete by worktree_id via removeTabFromWorktree. Otherwise (KEEP,
	// or REMOVE that degraded to no-worktree) fall through to the cheap
	// single-query unregisterTab.
	if wtForRemoval == nil {
		if worktreeLookupFailed {
			// We couldn't confirm the worktree, so we don't know if this
			// was its last reference. Dropping the link now would orphan
			// the dir invisibly: a zero-link worktree is never an orphan-GC
			// candidate (the >=1-link guard protects mid-creation rows), so
			// nothing would ever reclaim it. Leave the link instead — the
			// tab is closed, so it becomes a strand the orphan reconciler
			// reconciles and reaps once it confirms no live ref remains.
			return result
		}
		svc.unregisterTab(tabType, tabID)
		return result
	}

	svc.removeWorktreeIfLastReference(result, wtForRemoval, tabType, tabID)
	return result
}

// removeWorktreeIfLastReference drops this tab's worktree link and, once no
// referencing tab remains, removes the worktree from disk -- the REMOVE-close
// tail of closeTabCommon, factored out so the locked critical section reads as
// one unit. It stamps the WorktreeRemoval outcome (and any partial failure)
// onto result; result is never left UNSPECIFIED on this path.
//
// Serialize the re-check -> drop-link -> count -> remove sequence per worktree:
// DeleteBranchDialog fires every tab's REMOVE close concurrently, so without
// this lock two closes could both observe remaining == 0 and both shell out
// `git worktree remove`. Holding the lock across the git work is intentional --
// siblings of the SAME worktree must wait, while other worktrees use a
// different lock and never contend. The hold is deliberately NOT bounded by a
// timeout: a `git worktree remove` on a huge or busy tree can legitimately
// take a while, and we cannot assume an upper bound for a git operation; a
// premature timeout would abort a removal that is making progress. Only
// same-worktree closes wait, so the unbounded hold can never stall an
// unrelated tab. The same per-worktree lock guards ReapOrphanWorktree, so a
// close and the orphan GC for one worktree can never interleave.
func (svc *Context) removeWorktreeIfLastReference(result *leapmuxv1.CloseTabResult, wt *db.Worktree, tabType leapmuxv1.TabType, tabID string) {
	mu := svc.worktreeRemovalLock(wt.ID)
	mu.Lock()
	defer mu.Unlock()

	// wt was resolved by GetWorktreeForTab BEFORE we held the lock, so a
	// concurrent REMOVE close (a sibling tab) or the orphan GC may have torn
	// this worktree down in the meantime. Re-read the row under the lock and
	// bail if it is gone. This also defends a subtler hazard: the lock is
	// keyed by worktree id, and the unique partial index on worktree_path
	// (WHERE deleted_at IS NULL) means a directory cannot be re-adopted under
	// a NEW row until this row is soft-deleted -- so a soft-deleted/absent row
	// here is exactly the case where blindly running `git worktree remove
	// wt.WorktreePath` could rip out a freshly-adopted worktree that now owns
	// the same path under a different id (and a different lock). Bailing on a
	// gone row closes that window.
	switch latest, err := svc.Queries.GetWorktreeByID(bgCtx(), wt.ID); {
	case errors.Is(err, sql.ErrNoRows):
		// Hard-deleted (HardDeleteWorktreesBefore) after a prior soft-delete:
		// already gone, same as the soft-deleted case below.
		svc.unregisterTab(tabType, tabID)
		result.WorktreeRemoval = leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED
		return
	case err != nil:
		// Can't confirm the row state, so we can't safely remove. Surface a
		// partial failure rather than risk a double `git worktree remove`.
		slog.Warn("failed to re-read worktree under removal lock", "worktree_id", wt.ID, "error", err)
		setWorktreeRemovalFailed(result, wt, err)
		return
	case latest.DeletedAt.Valid:
		// Already removed by a concurrent close or the orphan GC. Drop our
		// now-dead link so it isn't left as a strand, and report the terminal
		// state -- the worktree the user asked to delete is gone.
		svc.unregisterTab(tabType, tabID)
		result.WorktreeRemoval = leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED
		return
	}

	if err := svc.removeTabFromWorktree(tabType, tabID, wt.ID); err != nil {
		// We couldn't drop THIS tab's link, so the count below would still
		// see it and wrongly conclude the worktree is still referenced --
		// silently leaking it when this was the last tab. Surface a partial
		// failure instead, symmetric with the count and remove failures below.
		slog.Warn("failed to drop worktree tab link during close", "worktree_id", wt.ID, "tab_id", tabID, "error", err)
		setWorktreeRemovalFailed(result, wt, err)
		return
	}
	remaining, countErr := svc.Queries.CountWorktreeTabs(bgCtx(), wt.ID)
	if countErr != nil {
		// We dropped this tab's link but can't confirm whether others remain,
		// so we can't safely remove. Surface it instead of returning a clean
		// result: if this was the last reference, the worktree is now orphaned
		// and the user must clean it up by hand.
		slog.Warn("failed to count worktree tabs after close", "worktree_id", wt.ID, "error", countErr)
		setWorktreeRemovalFailed(result, wt, countErr)
		return
	}
	if remaining != 0 {
		result.WorktreeRemoval = leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_STILL_REFERENCED
		return
	}
	if err := svc.removeWorktreeFromDisk(*wt, true); err != nil {
		setWorktreeRemovalFailed(result, wt, err)
		return
	}
	result.WorktreeRemoval = leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED
}

// setWorktreeRemovalFailed marks result as a failed worktree removal,
// stamping the path + id so the UI can point the user at the directory
// for manual cleanup. Shared by the link-drop, count, and `git worktree
// remove` failure paths, which all carry the same partial-failure shape.
func setWorktreeRemovalFailed(result *leapmuxv1.CloseTabResult, wt *db.Worktree, err error) {
	result.FailureMessage = "Failed to remove worktree"
	result.FailureDetail = err.Error()
	result.WorktreePath = wt.WorktreePath
	result.WorktreeId = wt.ID
	result.WorktreeRemoval = leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED
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
