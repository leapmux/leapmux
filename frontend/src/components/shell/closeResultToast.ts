import type { CloseTabResult } from '~/generated/proto/leapmux/v1/common_pb'
import { showWarnToast } from '~/components/common/Toast'
import { WorktreeAction, WorktreeRemovalOutcome } from '~/generated/proto/leapmux/v1/common_pb'

// WorktreeCloseSummary folds the per-tab WorktreeRemovalOutcome of a whole
// branch group's REMOVE closes (see closeWorktreeTabs) into one verdict the
// DeleteBranchDialog can toast. `unknown` covers tabs that returned no
// definitive outcome -- the close RPC was rejected, there was no worker to
// dispatch to, or the local close threw -- so the dialog can say "couldn't
// confirm" rather than implying a clean "not removed".
export interface WorktreeCloseSummary {
  removed: boolean
  failed: boolean
  stillReferenced: boolean
  unknown: boolean
}

// summarizeWorktreeCloses folds the per-tab WorktreeRemovalOutcome of one or
// more closes into a single verdict. A missing result means no definitive
// outcome for that tab: the close RPC was rejected, there was no worker to
// dispatch to, or the local close threw (each already warn-toasted its own
// detail). A worker-reported outcome is always a CloseTabResult -- even a
// degraded-to-KEEP close returns one with UNSPECIFIED -- so a missing result
// genuinely means "we do not know", and the caller reports "could not confirm"
// rather than a clean "not removed".
export function summarizeWorktreeCloses(results: readonly (CloseTabResult | undefined)[]): WorktreeCloseSummary {
  let removed = false
  let failed = false
  let stillReferenced = false
  let unknown = false
  for (const result of results) {
    if (!result) {
      unknown = true
      continue
    }
    switch (result.worktreeRemoval) {
      case WorktreeRemovalOutcome.REMOVED:
        removed = true
        break
      case WorktreeRemovalOutcome.FAILED:
        failed = true
        break
      case WorktreeRemovalOutcome.STILL_REFERENCED:
        stillReferenced = true
        break
    }
  }
  return { removed, failed, stillReferenced, unknown }
}

/**
 * Maps a folded worktree-close outcome plus whether the worktree was tracked
 * at inspect time to the info toast to show, or null when the caller must
 * stay silent (a FAILED close already warn-toasted its own git error and path
 * for manual cleanup).
 *
 * Both worktree-removal surfaces read it -- the Delete branch dialog for a
 * whole branch group, and the last-tab close for one tab -- so the copy names
 * the WORKTREE and never the tabs, which vary between them.
 *
 * Precedence is ground-truth-first: a real REMOVED / STILL_REFERENCED
 * outcome reported by the worker wins over the inspect-time
 * `trackedAtInspect` snapshot, which can be stale — the worktree may have
 * been adopted (gained a DB row) between inspect and confirm. It lives here,
 * beside the summary it consumes, so a hook does not import it from a dialog
 * component -- and so the precedence is unit-testable without rendering.
 */
export function worktreeRemovalToast(
  outcome: WorktreeCloseSummary,
  trackedAtInspect: boolean,
): string | null {
  if (outcome.removed) {
    // A close brought the worktree's ref-count to zero and the worker
    // removed it. Ground truth, so it wins over both the stale-snapshot
    // `trackedAtInspect` check and a sibling close's partial failure
    // (which already warn-toasted its own detail).
    return 'Worktree removed'
  }
  if (outcome.failed) {
    // The close pipeline already warn-toasted the git error and the
    // worktree path for manual cleanup (toastCloseFailure); don't also
    // claim success.
    return null
  }
  if (outcome.stillReferenced) {
    // A close dropped this tab's link but the worker still counted
    // siblings — tabs in another branch group, or a now-stale snapshot —
    // so it correctly kept the worktree. Only a tracked worktree can ever
    // report STILL_REFERENCED (an untracked one degrades REMOVE to KEEP),
    // so this wins over the stale empty-`worktreeId` snapshot below: a
    // worktree adopted between inspect and confirm is tracked-and-in-use,
    // not "untracked".
    return 'Worktree still in use elsewhere'
  }
  if (outcome.unknown) {
    // At least one close returned no definitive outcome — its RPC was
    // rejected, there was no worker to reach, or the local close threw
    // (each already warn-toasted its own detail). The worker may or may not
    // have removed the worktree, so we can't honestly claim either "removed"
    // or "not removed" — say it couldn't be confirmed. Ranks below the
    // definitive removed/failed/still-referenced signals (which come from
    // tabs that DID get a verdict) and above the stale inspect snapshot.
    return 'Could not confirm the worktree removal'
  }
  if (!trackedAtInspect) {
    // No DB row backed this worktree (created outside LeapMux via `git
    // worktree add`) and nothing removed it, so REMOVE degraded to KEEP
    // server-side and the dir stays on disk — say so rather than claiming
    // a removal.
    return 'Worktree kept: LeapMux does not track it'
  }
  // Tracked, but no close removed it, failed, or reported it still
  // referenced: every close degraded to KEEP because its worktree link was
  // already gone (e.g. a startup-race strand the worker's worktree GC will
  // reclaim). Nothing was removed — say so without implying another tab is
  // holding it.
  return 'Worktree not removed'
}

// toastCloseFailure surfaces a partial tab-close failure. No-op on
// success (empty failureMessage or missing result). The backend always
// pairs failureMessage with a failureDetail (err.Error()), but we guard
// against empty detail defensively.
export function toastCloseFailure(result: CloseTabResult | undefined): void {
  if (!result || !result.failureMessage)
    return
  showWarnToast(result.failureDetail ? `${result.failureMessage}: ${result.failureDetail}` : result.failureMessage)
}

// warnWorktreeUnreachable surfaces the "tab closed locally, but no
// worker connection so a REMOVE couldn't reach the worktree" warning.
// No-op for non-REMOVE actions. Centralizes the copy and the REMOVE
// guard that every close helper repeats when it has no worker to
// dispatch the close RPC to.
export function warnWorktreeUnreachable(worktreeAction: WorktreeAction): void {
  if (worktreeAction === WorktreeAction.REMOVE)
    showWarnToast('Closed the tab, but could not remove its worktree (no worker connection).')
}

// awaitCloseResult normalizes a worker close RPC into the shape the
// delete-branch flow consumes: toast any partial failure on success and
// resolve with the result; warn (with failLabel) and resolve undefined on
// RPC rejection. Folds the then/catch envelope every close helper repeated
// verbatim so they all report failures identically. The undefined returned on
// rejection is what closeWorktreeTabs reads as an "unknown" worktree outcome
// (the server-side removal state is genuinely indeterminate after a rejected
// RPC), distinct from a worker-reported UNSPECIFIED (a definitive no-op).
export function awaitCloseResult(
  rpc: Promise<{ result?: CloseTabResult }>,
  failLabel: string,
): Promise<CloseTabResult | undefined> {
  return rpc
    .then((resp) => {
      toastCloseFailure(resp.result)
      return resp.result
    })
    .catch((err) => {
      showWarnToast(failLabel, err)
      return undefined
    })
}
