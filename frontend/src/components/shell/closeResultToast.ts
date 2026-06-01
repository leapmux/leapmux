import type { CloseTabResult } from '~/generated/leapmux/v1/common_pb'
import { showWarnToast } from '~/components/common/Toast'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'

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
