import type { TabStampTarget } from './syncGitStatusToTabs'
import type { TabContext } from './tabContext'
import type { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import { getGitFileStatus } from '~/api/workerRpc'
import { createLogger } from '~/lib/logger'
import { isSameRepo } from '~/stores/tab.helpers'
import { stampBranchOnTabs } from './stampBranchOnTabs'
import { applyGitStatusToTabs, gitStatusFromStore } from './syncGitStatusToTabs'

const log = createLogger('handleBranchChanged')

/**
 * What has to happen after a Change branch / Delete branch succeeds.
 *
 * Two steps, and the second forks:
 *
 *   1. Stamp the new branch label on every tab in `(workerId, gitToplevel)`,
 *      in ONE pass over every workspace. This used to be the active store's
 *      stamp plus a fan-out across each registry snapshot — the fan-out existed
 *      solely so a Change branch opened on an INACTIVE workspace's sidebar row
 *      didn't leave that workspace's label stale until the next switch.
 *      `isSameRepo` refuses an empty `gitToplevel`, so an unresolved repo path
 *      stamps nothing rather than every un-stamped tab on the worker.
 *
 *   2. Refresh diff stats. For the ACTIVE repo, refresh the file-status
 *      singleton so the file tree updates and `syncGitStatusToTabs` cascades
 *      into tab metadata. For any other repo, fetch directly and stamp, but do
 *      NOT touch the singleton: it tracks the focused repo's file tree, and a
 *      non-focused refresh would flip the tree to a repo the user is not
 *      looking at.
 *
 * Either way the stamp reaches every workspace's tabs — that is how a
 * non-visible workspace's sidebar diff badges pick up post-branch-change state
 * without waiting to be switched in.
 *
 * Extracted from a closure inline in an `AppShellDialogs` JSX prop, where it
 * was the largest piece of business logic in `AppShell` and reachable only by
 * rendering the dialog. `TabStampTarget` is the seam that lets it be driven
 * directly, with no CRDT bridge and no reactive root.
 */
export interface BranchChangedDeps {
  target: TabStampTarget
  gitFileStatusStore: ReturnType<typeof createGitFileStatusStore>
  getCurrentTabContext: () => TabContext
}

export function handleBranchChanged(
  deps: BranchChangedDeps,
  workerId: string,
  gitToplevel: string,
  newBranch: string,
): void {
  stampBranchOnTabs(deps.target, workerId, gitToplevel, newBranch)

  if (isSameRepo(deps.getCurrentTabContext(), workerId, gitToplevel)) {
    void deps.gitFileStatusStore.refresh(workerId, gitToplevel)
      .then(() => {
        // Reuse the singleton's freshly-refreshed state rather than firing a
        // second getGitFileStatus.
        applyGitStatusToTabs(deps.target, gitStatusFromStore(deps.gitFileStatusStore.state))
      })
      // `refresh` swallows its own RPC failure, but the continuation above does
      // not: it walks every tab in the account and writes metadata, so a throw
      // becomes an unhandled rejection with no toast and no diagnosis. The
      // non-active arm below logs the same class of failure; two arms of one
      // function should not answer that differently.
      .catch((err) => {
        log.warn('failed to refresh git status for active repo', err)
      })
    return
  }

  void getGitFileStatus(workerId, { workerId, path: gitToplevel })
    .then((resp) => {
      // `toplevel` is authoritative: the worker sets it on every success path
      // (`git.go` queryGitPathInfo), and a non-repo path returns it empty
      // alongside an empty repo_root. An empty value therefore means "no
      // working tree", which `applyGitStatusToTabs` correctly declines to stamp.
      applyGitStatusToTabs(deps.target, {
        workerId,
        toplevel: resp.toplevel,
        originUrl: resp.originUrl,
        currentBranch: resp.currentBranch,
        files: resp.files,
      })
    })
    .catch((err) => {
      log.warn('failed to refresh git status for non-active repo', err)
    })
}
