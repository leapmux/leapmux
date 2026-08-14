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

// closeWorktreeDisposition is what a close decided about a worktree the tab's
// startup created, in the one case the close itself cannot act on it: the
// startup goroutine is still running, so the worktree_tabs link the close
// looks for may not be written yet.
//
// It exists because the outcome of closing a tab must NOT depend on whether
// the close raced startup. Without it the startup goroutine treated every
// close as a failed startup and force-removed the worktree -- discarding a
// directory the user had just been shown a dialog about and explicitly asked
// to keep, along with any uncommitted work in it.
type closeWorktreeDisposition int

const (
	// keepWorktreeOnClose leaves the worktree alone: the close pinned KEEP,
	// either as the user's explicit "Close anyway" or as the offline
	// fallback. The link is never written, so the worktree ends at zero
	// links -- exactly what an online KEEP close of a fully-started tab
	// leaves behind, and deliberately excluded from orphan GC.
	keepWorktreeOnClose closeWorktreeDisposition = iota
	// removeWorktreeOnClose rolls the worktree back on the close's behalf.
	// The user confirmed REMOVE in the last-tab (or delete-branch) dialog,
	// which is the one path allowed to discard uncommitted work -- they were
	// shown the dirty/unpushed counts first. The close could not honour it
	// itself because the link did not exist yet.
	removeWorktreeOnClose
	// strandWorktreeOnClose writes the link even though the tab is already
	// closed, so the row becomes the strand ListOrphanCandidateWorktrees
	// selects. Reserved for a convergence close of a DELETED workspace: no
	// user intent for the directory survives, so the orphan reconciler
	// reclaims it under its own unsaved-work probe rather than this
	// goroutine force-removing it unasked.
	strandWorktreeOnClose
)

// closeWorktreeDispositionFor derives the disposition from the two things a
// close already states. Kept next to both enums so the mapping cannot drift
// from what dropWorktreeLink / keepWorktreeLinkForReconciler mean.
func closeWorktreeDispositionFor(action leapmuxv1.WorktreeAction, linkPolicy worktreeLinkPolicy) closeWorktreeDisposition {
	// Checked first: closeTabForConvergence pins the action to UNSPECIFIED, so
	// the policy is the only thing that distinguishes a deleted workspace from
	// a reconciler reap of a single tab.
	if linkPolicy == keepWorktreeLinkForReconciler {
		return strandWorktreeOnClose
	}
	if action == leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE {
		return removeWorktreeOnClose
	}
	return keepWorktreeOnClose
}

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
			setWorktreeRemovalRefused(result, wt, "Worktree kept: it may hold unsaved work", reason)
			return
		}
	}
	// Enforce the removal policy HERE, at the site that runs the removal, and
	// not only at the clients that offered the choice. The clients read the
	// same verdict to disable a button and to refuse a flag, but a client that
	// ignores the field still reaches this line, and a `git worktree lock`
	// taken after the dialog opened beats any answer they were given. This
	// check runs inside the per-worktree removal lock the caller already holds,
	// so it cannot be raced, and it reports the curated sentence rather than
	// git's raw stderr.
	//
	// A probe that cannot answer falls through to the removal. That matches the
	// fail-open rule worktreeRemovalBlockedReason states: a block the worker
	// cannot confirm would refuse a removal the user can have.
	if reason, err := svc.worktreeRemovalBlockedReason(bgCtx(), wt.WorktreePath, nil); err != nil {
		slog.Warn("worktree removal policy could not be read; attempting the removal",
			"worktree_id", wt.ID, "worktree_path", wt.WorktreePath, "error", err)
	} else if reason != "" {
		slog.Info("refusing a worktree removal git would reject",
			"worktree_id", wt.ID, "worktree_path", wt.WorktreePath, "reason", reason)
		setWorktreeRemovalRefused(result, wt, "Worktree kept: git refuses to remove it", reason)
		return
	}
	if err := svc.removeWorktreeFromDisk(*wt); err != nil {
		setWorktreeRemovalFailed(result, wt, err)
		return
	}
	result.WorktreeRemoval = leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED
}

// setWorktreeRemovalRefused marks a removal the worker declined on purpose --
// as opposed to one it attempted and failed. Reported as FAILED so the UI
// surfaces it rather than implying the directory is gone.
//
// message names WHY this call refused (unsaved work nobody confirmed, or a
// removal git itself would reject); reason carries the specifics and renders
// after the message in the close-failure toast.
//
// The directory survives, but nothing revisits it: this close already dropped
// the last worktree_tabs link (that is what `remaining == 0` above means), and
// ListOrphanCandidateWorktrees requires at least one link, so the orphan
// reconciler never sees the worktree again. The user cleans it up by hand.
func setWorktreeRemovalRefused(result *leapmuxv1.CloseTabResult, wt *db.Worktree, message, reason string) {
	result.FailureMessage = message
	result.FailureDetail = reason
	result.WorktreePath = wt.WorktreePath
	result.WorktreeId = wt.ID
	result.WorktreeRemoval = leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED
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
// spawnControlIPC teardown, so the tab's unix-socket listener stayed open and its
// delegation token stayed UNREVOKED for the life of the worker process. This
// commit made that offline path the normal one, which is what turned a
// discrepancy into a leak.
// Returns the close result and the agent's DESCENDANT subagent ids, at any
// depth and in no particular order (the query walks the tree breadth-first, so
// a parent comes BEFORE its own children). An agent id is also its tab id, so
// the caller retires those tabs alongside the one it closed -- see
// CloseAgentResponse.descendant_agent_ids for why they cannot outlive it, and
// for the filter every caller must apply.
//
// For a ROOT the list is read AFTER the teardown, and that is the point. The
// provider keeps creating child rows while it drains: `EnsureChildAgent` fires
// from the stdout scan loop, and `StopAgent` returns only once that loop is
// done. A list captured at entry therefore misses a subagent spawned during the
// drain -- which would then never have its per-agent maps freed, never be
// reported to a client, and never be reaped, because the orphan reconciler
// walks roots only. One read after the teardown answers both consumers, and
// replaces the second recursive walk this function used to run.
//
// A child close reads its own list early: it runs no teardown at all. That
// close is UI-only, but the child's own subagents still go with it.
func (svc *Service) closeAgentTabCommon(userID, agentID string, action leapmuxv1.WorktreeAction, linkPolicy worktreeLinkPolicy) (*leapmuxv1.CloseTabResult, []string) {
	// Assigned by rootClose below for a root, and by the child branch for a
	// child. Read in the return statement, which runs after closeTabCommon.
	var descendants []string

	// A virtual child agent (a subagent transcript): closing its tab is
	// UI-only. Run NO teardown, stamp NO closed_at, and return an empty
	// successful result. Transcript + registry survive (they live under the
	// root); reopening the tab re-hydrates from the still-open row.
	dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err == nil && dbAgent.ParentAgentID.Valid {
		childDescendants, derr := svc.Queries.ListDescendantAgentIDs(bgCtx(), sql.NullString{String: agentID, Valid: true})
		if derr != nil {
			// Only the caller's tab cleanup is lost, and an orphaned subagent tab
			// is recoverable (the user closes it).
			slog.Warn("close agent: list descendants failed", "agent", agentID, "err", derr)
			childDescendants = nil
		}
		return &leapmuxv1.CloseTabResult{}, childDescendants
	}

	rootTeardown := func() {
		svc.AgentStartup.cancelAndClear(agentID, closeWorktreeDispositionFor(action, linkPolicy))
		// Close the root AND every virtual descendant in one tree. Closing only
		// the root row would orphan child rows (they have no worktree_tabs link
		// and are invisible to the reconciler). Free each child's span
		// tracker/caches alongside the row teardown.
		svc.Agents.StopAgent(agentID)
		svc.Output.ClearAgentRuntimeState(agentID)
		svc.agentCleanups.run(agentID)
		// Prune all child sinks and per-child state in one pass. Calling
		// CleanupAgent per child would scan rootSinks for each (O(children x
		// roots) on a root close); the root is known here, so clear its whole
		// childSinks map once and delete the cheap per-agent maps per child.
		svc.Output.ForgetChildSinks(agentID)
		// The per-child maps are freed in rootClose, which is where the child
		// list is read -- see this function's doc. ForgetChildSinks stays here
		// because it is keyed by the ROOT and needs no list at all.
		//
		// A close with no running process still gives the registry rows a final status
		// as 'stopped' (a deliberate user action).
		svc.Output.MarkAgentBackgroundTasksExited(agentID, true)
	}
	rootClose := func() (bool, error) {
		// Stamp closed_at on the root + all open descendants via the tree id
		// list (sqlc cannot parse a recursive CTE inside an UPDATE's
		// IN-subquery). CloseAgent's closed_at IS NULL guard makes each step
		// idempotent.
		ids, err := svc.Queries.ListAgentTreeIDs(bgCtx(), agentID)
		if err != nil {
			return false, err
		}
		// The tree minus its root IS the descendant list, so this one walk
		// serves both the in-memory cleanup and the ids reported to the caller.
		descendants = make([]string, 0, len(ids))
		for _, id := range ids {
			if id != agentID {
				descendants = append(descendants, id)
			}
		}
		svc.Output.CleanupChildAgents(descendants)
		var anyRetired bool
		// Continue past a transient error on one id so a single failed
		// CloseAgent does not orphan the remaining descendants. The orphan
		// reconciler lists only roots, so an unclosed child row is never
		// reaped; closing every id we can keeps the tree consistent. The first
		// error is returned so the caller still sees the failure.
		var firstErr error
		for _, id := range ids {
			retired, affErr := rowsAffected(svc.Queries.CloseAgent(bgCtx(), id))
			if affErr != nil {
				// Record the first error for the caller; log every error so a
				// later failure is not silently swallowed after the first.
				if firstErr == nil {
					firstErr = affErr
				} else {
					slog.Warn("background task tree close: descendant close failed",
						"agent", agentID, "descendant", id, "err", affErr)
				}
				continue
			}
			anyRetired = anyRetired || retired
		}
		return anyRetired, firstErr
	}
	return svc.closeTabCommon(
		leapmuxv1.TabType_TAB_TYPE_AGENT,
		agentID,
		userID,
		action,
		linkPolicy,
		rootTeardown,
		rootClose,
	), descendants
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
			svc.TerminalStartup.cancelAndClear(terminalID, closeWorktreeDispositionFor(action, linkPolicy))
			svc.Terminals.RemoveTerminal(terminalID)
			svc.clearTerminalBellCoalesce(terminalID)
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
		// The descendant ids go unused: a convergence close has no client to
		// report them to, and the CRDT tab records are already gone -- that IS
		// what drove this close.
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
