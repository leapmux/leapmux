package service

import (
	"database/sql"
	"errors"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// worktreeLinkPolicy decides what a non-REMOVE close does with the tab's
// worktree_tabs row. The zero value is dropWorktreeLink, the historical
// behaviour -- but Go has no default arguments, so every call site states the
// policy explicitly and none can inherit it by omission.
type worktreeLinkPolicy int

const (
	// dropWorktreeLink unregisters the tab, which is right for a KEEP close of
	// a single tab: the user asked to keep the worktree, and a zero-link
	// worktree is deliberately excluded from orphan GC, so the directory
	// survives until they remove it themselves.
	dropWorktreeLink worktreeLinkPolicy = iota
	// keepWorktreeLinkForReconciler leaves the row as a strand so the orphan
	// reconciler can reclaim the worktree. Correct ONLY when the thing that
	// referenced the worktree is going away entirely -- a deleted workspace --
	// because there is no longer any user intent for the directory to serve.
	//
	// Its counterpart above is not a weaker version of this: the two encode
	// OPPOSITE intents. Dropping the link means "keep the directory, hide it from
	// GC"; keeping it means "no one wants this, reclaim it once nothing live
	// refers to it". A reconciler reap of a closed tab is the first, not the
	// second -- see CloseAgentTabForReconcile.
	keepWorktreeLinkForReconciler
)

// closeFileTabCommon drives the shared closeTabCommon flow for FILE
// tabs. It exists so the FILE close path uses the same worktree-tab
// link drop and conditional `git worktree remove` machinery as
// CloseAgent / CloseTerminal — the only file-tab specific work is
// dropping the worker_file_tab row (which doubles as the
// FileTabPathRevoked emit). stopProcess is a noop because file tabs
// own no process on the worker.
//
// Two callers: the RevokeFileTabPath RPC (with dropWorktreeLink) and the orphan
// reconciler via CloseFileTabForReconcile (with keepWorktreeLinkForReconciler).
// The reconciler used to hand-roll its own teardown here to avoid the
// worktree-removal branch, but an UNSPECIFIED action never enters that branch
// anyway -- and dropping the link, as the hand-rolled version did, strands the
// worktree directory permanently.
func (svc *Service) closeFileTabCommon(userID, tabID string, action leapmuxv1.WorktreeAction, linkPolicy worktreeLinkPolicy) *leapmuxv1.CloseTabResult {
	return svc.closeTabCommon(
		leapmuxv1.TabType_TAB_TYPE_FILE,
		tabID,
		userID,
		action,
		linkPolicy,
		func() {},
		func() (bool, error) {
			err := svc.FileTabPaths.RevokeRow(bgCtx(), userID, tabID)
			// Idempotent: the row may have been deleted by a concurrent close, or
			// by a CleanupWorkspace that left the worktree link as a strand.
			// closeTabCommon proceeds to drop the worktree link regardless -- but
			// it now learns that no live row was retired, which is what stops a
			// REMOVE from force-removing a directory nobody was asked about.
			if errors.Is(err, ErrFileTabPathNotFound) {
				return false, nil
			}
			return err == nil, err
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
//
// userID is the authenticated caller. It scopes every worktree_tabs read and
// delete below, because a FILE tab's id is unique only within a user -- an
// owner-blind delete would detach a DIFFERENT user's still-open file tab from
// the same worktree. AGENT/TERMINAL callers may pass "" or the real user;
// worktreeTabUserID normalizes both to the "" those links were written with.
func (svc *Service) closeTabCommon(
	tabType leapmuxv1.TabType,
	tabID string,
	userID string,
	action leapmuxv1.WorktreeAction,
	linkPolicy worktreeLinkPolicy,
	stopProcess func(),
	closeDB func() (retiredLiveRow bool, err error),
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
			UserID:  worktreeTabUserID(tabType, userID),
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

	retiredLiveRow, err := closeDB()
	if err != nil {
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
		if linkPolicy == keepWorktreeLinkForReconciler {
			// Same reasoning as the worktreeLookupFailed branch above: keep the
			// strand so the reconciler still sees a candidate.
			return result
		}
		svc.unregisterTab(tabType, tabID, userID)
		return result
	}

	svc.removeWorktreeIfLastReference(result, wtForRemoval, tabType, tabID, userID, retiredLiveRow)
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
func (svc *Service) removeWorktreeIfLastReference(result *leapmuxv1.CloseTabResult, wt *db.Worktree, tabType leapmuxv1.TabType, tabID, userID string, retiredLiveRow bool) {
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
		svc.unregisterTab(tabType, tabID, userID)
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
		svc.unregisterTab(tabType, tabID, userID)
		result.WorktreeRemoval = leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED
		return
	}

	if err := svc.removeTabFromWorktree(tabType, tabID, userID, wt.ID); err != nil {
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
	// A REMOVE that did not retire a LIVE row is not a user-confirmed delete.
	// The row was already closed, so whoever sent this is a stale client -- a
	// peer session that has not converged past a workspace tombstone, or a
	// DeleteBranchDialog whose snapshot predates a CleanupWorkspace -- and the
	// dialog it saw described a tab the worker already considers gone. Nobody
	// was shown this directory's dirty/unpushed state, so apply the same probe
	// ReapOrphanWorktree uses for its unattended reap.
	//
	// The live-row case is deliberately NOT probed: there the user saw the
	// last-tab dialog with the dirty and unpushed counts and chose Delete
	// anyway, and second-guessing that would make the button lie.
	if !retiredLiveRow {
		if hasWork, reason := svc.worktreeHoldsUnsavedWork(bgCtx(), *wt); hasWork {
			slog.Info("skipping worktree removal for an already-closed tab, worktree may hold unsaved work",
				"worktree_id", wt.ID, "worktree_path", wt.WorktreePath, "reason", reason)
			setWorktreeRemovalRefused(result, wt, reason)
			return
		}
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
// setWorktreeRemovalRefused marks a removal we declined on purpose, because the
// close could not be attributed to a live tab and the directory still holds work
// nobody was asked about. Reported as FAILED so the UI surfaces it rather than
// implying the directory is gone; the link is left in place, so the orphan
// reconciler re-examines it and reclaims it once the work is committed and
// pushed.
func setWorktreeRemovalRefused(result *leapmuxv1.CloseTabResult, wt *db.Worktree, reason string) {
	result.FailureMessage = "Worktree kept: it may hold unsaved work"
	result.FailureDetail = reason
	result.WorktreePath = wt.WorktreePath
	result.WorktreeId = wt.ID
	result.WorktreeRemoval = leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED
}

func setWorktreeRemovalFailed(result *leapmuxv1.CloseTabResult, wt *db.Worktree, err error) {
	result.FailureMessage = "Failed to remove worktree"
	result.FailureDetail = err.Error()
	result.WorktreePath = wt.WorktreePath
	result.WorktreeId = wt.ID
	result.WorktreeRemoval = leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED
}

// rowsAffected adapts an :execresult close to closeTabCommon's
// (retiredLiveRow, err) contract. CloseAgent / CloseTerminal carry
// `AND closed_at IS NULL`, so zero affected rows means the row was already
// closed -- the tab was not live and this close retired nothing.
func rowsAffected(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
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

// closeAgentTabCommon is the whole teardown for one agent tab: cancel any
// in-flight startup, stop the subprocess, clear its runtime state, run its
// registered cleanups, and close the DB row.
//
// Extracted so the ONLINE close (CloseAgent), the batch close
// (handleCleanupWorkspace) and the OFFLINE convergence path
// (OrphanReconciler.reconcileAgents) cannot diverge. They had: the reconciler
// ran only the DB close plus StopAgent, omitting AgentStartup.cancelAndClear
// (so a startup racing the reap kept running and could rewrite the rows just
// closed), Output.ClearAgentRuntimeState, and agentCleanups.run -- which is the
// spawnRemoteIPC teardown, so the tab's unix-socket listener stayed open and its
// delegation token stayed UNREVOKED for the life of the worker process. This
// commit made that offline path the normal one, which is what turned a
// discrepancy into a leak.
func (svc *Service) closeAgentTabCommon(userID, agentID string, action leapmuxv1.WorktreeAction, linkPolicy worktreeLinkPolicy) *leapmuxv1.CloseTabResult {
	return svc.closeTabCommon(
		leapmuxv1.TabType_TAB_TYPE_AGENT,
		agentID,
		userID,
		action,
		linkPolicy,
		func() {
			svc.AgentStartup.cancelAndClear(agentID)
			svc.Agents.StopAgent(agentID)
			svc.Output.ClearAgentRuntimeState(agentID)
			svc.agentCleanups.run(agentID)
		},
		func() (bool, error) { return rowsAffected(svc.Queries.CloseAgent(bgCtx(), agentID)) },
	)
}

// closeTerminalTabCommon is the terminal mirror of closeAgentTabCommon. Note it
// uses RemoveTerminal, not StopTerminal: the latter signals the process but
// leaves the manager's terminals/meta/exitDone entries behind, which the
// reconciler used to do and which leaked one entry per reaped terminal.
func (svc *Service) closeTerminalTabCommon(userID, terminalID string, action leapmuxv1.WorktreeAction, linkPolicy worktreeLinkPolicy) *leapmuxv1.CloseTabResult {
	return svc.closeTabCommon(
		leapmuxv1.TabType_TAB_TYPE_TERMINAL,
		terminalID,
		userID,
		action,
		linkPolicy,
		func() {
			svc.TerminalStartup.cancelAndClear(terminalID)
			svc.Terminals.RemoveTerminal(terminalID)
			svc.terminalCleanups.run(terminalID)
		},
		func() (bool, error) { return rowsAffected(svc.Queries.CloseTerminal(bgCtx(), terminalID)) },
	)
}

// closeTabForConvergence is the single entry point for a close nobody is waiting
// on: the orphan reconciler's reap, and the teardown of a deleted workspace's
// tabs. It owns the per-type dispatch and pins the worktree action to UNSPECIFIED,
// so a convergence close cannot ask for a worktree removal -- the Hub's "this tab
// is gone" says nothing about whether the directory should go, and the
// worktree-removal branch is reserved for a user who was shown a dialog.
//
// Pinning it here is what makes the illegal pairing unrepresentable rather than
// merely absent: REMOVE + keep-link silently ignored the policy, and every caller
// used to restate both arguments by hand at five sites.
//
// linkPolicy is the one axis that genuinely varies, and the two callers below name
// which they are, so no third caller has to work it out.
func (svc *Service) closeTabForConvergence(
	tabType leapmuxv1.TabType,
	userID, tabID string,
	linkPolicy worktreeLinkPolicy,
) {
	const action = leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED
	switch tabType {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		svc.closeAgentTabCommon(userID, tabID, action, linkPolicy)
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		svc.closeTerminalTabCommon(userID, tabID, action, linkPolicy)
	case leapmuxv1.TabType_TAB_TYPE_FILE:
		// A file tab owns no process, so this reduces to dropping its row.
		svc.closeFileTabCommon(userID, tabID, action, linkPolicy)
	case leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED:
		slog.Warn("convergence close: unsupported tab type", "tab_id", tabID, "tab_type", tabType)
	}
}

// CloseTabForReconcile is the orphan reconciler's entry point: the OFFLINE half of
// a user's own tab close.
//
// It drops the worktree link, which is how KEEP is expressed -- a zero-link
// worktree is excluded from ListOrphanCandidateWorktrees, so the directory
// survives until the user removes it themselves, exactly as an online KEEP close
// leaves it. An offline close pins KEEP, so honouring it here is what stops the
// offline path from destroying a clean worktree the identical online close kept.
//
// userID is empty for AGENT and TERMINAL, whose links are written owner-blind; a
// FILE tab's id is unique only within a user, so its owner is required.
func (svc *Service) CloseTabForReconcile(tabType leapmuxv1.TabType, userID, tabID string) {
	svc.closeTabForConvergence(tabType, userID, tabID, dropWorktreeLink)
}

// closeTabForDeletedWorkspace is handleCleanupWorkspace's entry point, and the one
// case that KEEPS the link as a strand: the workspace the directory belonged to is
// gone, so no user intent for it survives and the strand is what leaves it a GC
// candidate for the worktree pass to reclaim under its unsaved-work probe.
func (svc *Service) closeTabForDeletedWorkspace(tabType leapmuxv1.TabType, userID, tabID string) {
	svc.closeTabForConvergence(tabType, userID, tabID, keepWorktreeLinkForReconciler)
}
