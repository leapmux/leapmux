import type { TabStampTarget } from './syncGitStatusToTabs'
import type { RepoRef } from '~/stores/tab.helpers'
import { isSameRepo } from '~/stores/tab.helpers'

/**
 * Re-label every tab in one repo with a branch name, immediately.
 *
 * A Change/Delete branch dialog knows the new branch the moment its RPC
 * returns, and the sidebar groups tabs by branch — so waiting for the next
 * git-status refresh would leave the tree showing the old label for as long as
 * the poll interval. This writes it straight through instead.
 *
 * Identity is `(workerId, gitToplevel)`, matched by {@link isSameRepo}, which
 * refuses an empty `repoToplevel` — an unresolved repo path stamps nothing
 * rather than every tab on the worker that hasn't learned its toplevel yet.
 * BOTH halves must be non-empty for the same reason: an empty `workerId`
 * matches every tab whose own worker has not resolved yet, and since the scope
 * below is account-wide that would re-label tabs in every workspace with a
 * branch they are not on.
 *
 * Scope is every workspace's tabs, not just the visible one: the dialog can be
 * opened from any workspace's sidebar row, and its repo's tabs may live in
 * several workspaces at once.
 *
 * @returns whether any tab was stamped.
 */
export function stampBranchOnTabs(
  target: TabStampTarget,
  repo: RepoRef,
  newBranch: string,
): boolean {
  if (!repo.workerId)
    return false
  const matches = target.tabs.filter(
    t => isSameRepo(t, repo) && t.gitBranch !== newBranch,
  )
  if (matches.length === 0)
    return false
  target.update(new Set(matches.map(t => t.id)), { gitBranch: newBranch })
  return true
}
