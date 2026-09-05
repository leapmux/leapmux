import type { RepositoryCheckout } from './RepositoryMenuItems'
import type { BranchGroup } from './WorkspaceTabTree'
import { compositeKey } from './branchKeys'

/**
 * One checkout of a repository: a working-tree root on one Worker, with
 * everything the repository row's menu acts on.
 *
 * It extends the shared block's {@link RepositoryCheckout} rather than
 * repeating its three fields, so the block and this projection cannot drift
 * about what a checkout IS.
 */
export interface RepoCheckout extends RepositoryCheckout {
  workerId: string
  /** What the submenu row reads: the branch label, marked when it is a worktree. */
  label: string
  /** The branch group this checkout came from, for binding the row's actions. */
  branch: BranchGroup
}

/**
 * The distinct checkouts under one repository group.
 *
 * A repository group is keyed by repo IDENTITY -- the origin URL -- so it can
 * hold the main clone and any number of linked worktrees, and the same clone
 * on two Workers. The branch rows under it are keyed by
 * `(branchName, workerId, gitToplevel)`, so several of them can share one
 * checkout; this collapses those to the `(workerId, gitToplevel)` pairs, which
 * is what a path-shaped action acts on.
 *
 * First branch wins for the label, matching the tree's own order, so the row
 * the user sees first is the one the submenu names.
 */
export function listRepoCheckouts(
  branches: readonly BranchGroup[],
  originUrl: string,
  isLocalWorker: (workerId: string) => boolean,
): RepoCheckout[] {
  const byPath = new Map<string, RepoCheckout>()
  for (const branch of branches) {
    // A branch group with no resolved toplevel has no path to act on: every
    // item of the menu it would produce needs one.
    if (!branch.gitToplevel)
      continue
    const key = compositeKey(branch.workerId, branch.gitToplevel)
    if (byPath.has(key))
      continue
    byPath.set(key, {
      workerId: branch.workerId,
      gitToplevel: branch.gitToplevel,
      originUrl,
      isLocal: isLocalWorker(branch.workerId),
      label: checkoutLabel(branch),
      branch,
    })
  }
  return [...byPath.values()]
}

/**
 * What one checkout's submenu row reads.
 *
 * `displayLabel` already carries whatever disambiguation the tree needed --
 * the worker, the path, or both -- so this only adds the worktree mark, which
 * is the fact that distinguishes two checkouts of one repository.
 */
function checkoutLabel(branch: BranchGroup): string {
  return branch.isWorktree ? `${branch.displayLabel} (worktree)` : branch.displayLabel
}
