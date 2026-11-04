import type { TabType, WorktreeAction } from '~/generated/proto/leapmux/v1/common_pb'
import type { CloseTabResponse } from '~/generated/proto/leapmux/v1/worker_tab_pb'
import { workerTab } from '~/api/workerRpc'
import { awaitCloseResult, warnWorktreeUnreachable } from './closeResultToast'

/**
 * Send the close of one tab to the worker that hosts it.
 *
 * Every tab type closes through this one sequence, after the caller has
 * removed the tab locally: the tab leaves the screen at once, and the worker
 * close runs behind it. The caller keeps the local cleanup that its tab type
 * needs.
 *
 * Resolves with the worker's response, or with undefined when no response
 * came:
 * - With no worker to send the close to, a REMOVE cannot reach the worktree,
 *   so a warning says so instead of letting the caller assume a removal.
 * - A rejected RPC shows `failLabel` with the error.
 *
 * The delete-branch flow reads that undefined as an unknown worktree outcome.
 * The type the request sends is a fallback only: the worker reads the kind of
 * the tab from the row that it deletes.
 */
export function closeTabOnWorker(
  workerId: string,
  tab: { type: TabType, id: string },
  worktreeAction: WorktreeAction,
  failLabel: string,
): Promise<CloseTabResponse | undefined> {
  if (!workerId) {
    warnWorktreeUnreachable(worktreeAction)
    return Promise.resolve(undefined)
  }
  return awaitCloseResult(workerTab.closeTab(workerId, {
    tab: { tabType: tab.type, tabId: tab.id },
    worktreeAction,
  }), failLabel)
}
