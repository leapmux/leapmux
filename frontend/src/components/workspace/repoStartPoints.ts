import type { WorkspaceRepoStartPoint } from './workspaceStartPoint'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { RepoGitStore, RepoKey } from '~/stores/repoGit'
import type { Tab } from '~/stores/tab.types'
import { tildifyForWorker } from '~/lib/workerPaths'
import { repoGitView, repoKey } from '~/stores/repoGit'
import { compositeKey, repoKeyAndLabel, tabGitToplevelForKey } from './branchKeys'

/**
 * A repository a section already works on, ready to start a workspace in.
 *
 * The recency-ordered sibling of `sumDiffStatsFromTabs`: both answer a question
 * about a tab list without building the full tab tree. `buildTree` allocates
 * two collision passes, per-branch diff sums and a Map per repository, and it
 * sorts remote-before-local then by label -- and a header menu wants the
 * repositories the user touched most recently.
 */
export interface RepoStartPoint {
  /**
   * What the row reads.
   *
   * `<repository>` normally, `<worker> · <repository>` once the list spans more
   * than one worker, and either of those plus a tilde-compressed path once two
   * entries would otherwise read identically. The same "add context only where
   * it disambiguates" rule the branch rows follow.
   */
  label: string
  /** Where a workspace started here begins. */
  startPoint: WorkspaceRepoStartPoint
}

export interface ListRepoStartPointsOptions {
  /** Worker display info, for the worker segment of a label and its home directory. */
  workerInfoFn?: (id: string) => WorkerInfo | null
  /**
   * Whether a worker can be reached. A repository on an unreachable worker is
   * OMITTED, because its row opens a dialog that cannot probe the path, cannot
   * list its branches and cannot start an agent -- a trap rather than a
   * shortcut. `isWorkerKnownOnline` is the intended argument: it fails open, so
   * a fleet list that has not arrived hides nothing.
   */
  isWorkerOnline?: (workerId: string) => boolean
  /** Keep at most this many entries, most recent first. */
  limit?: number
}

/** One checkout: every tab that shares a (worker, toplevel) pair. */
interface Checkout {
  workerId: string
  gitToplevel: string
  /** Repository identity, so two checkouts of one repository can be compared. */
  repoKey: string
  repoLabel: string
  isWorktree: boolean
  currentBranch: string
  /** The highest activation counter across the checkout's tabs, or -1 for none. */
  mru: number
  /** The newest `createdAt` across the checkout's tabs. */
  createdAt: string
}

/**
 * The repositories `tabs` are checked out in, most recently used first.
 *
 * The input is ONE SECTION's tabs, so a section's menu offers the repositories
 * that section works on rather than every repository in the account.
 *
 * This is a SESSION view, not a durable registry. It reads `repoGitStore`,
 * which is capped at 256 entries with LRU eviction and fills as tabs hydrate,
 * so the answer means "the repositories this session has seen" and it grows
 * over the life of the page.
 *
 * A linked worktree is dropped when its own repository also has a normal
 * checkout here: the dialog can create a worktree from that checkout, so
 * listing both is one place to start too many. A repository known ONLY through
 * its worktrees keeps them, because they are the only places anyone knows.
 */
export function listRepoStartPoints(
  tabs: readonly Tab[],
  store: RepoGitStore,
  opts: ListRepoStartPointsOptions = {},
): RepoStartPoint[] {
  const checkouts = new Map<RepoKey, Checkout>()

  for (const tab of tabs) {
    const workerId = tab.workerId
    if (!workerId)
      continue
    // Resolved ONCE per tab and threaded into both helpers below. Each
    // resolution may run the canonical-repo-key scan for a toplevel-less tab.
    const git = repoGitView(tab, store)
    const rk = repoKeyAndLabel(tab, store, git)
    if (!rk)
      continue
    const gitToplevel = tabGitToplevelForKey(tab, store, git)
    if (!gitToplevel)
      continue

    // The git store's own key space for a checkout, so this bucket and the
    // store cannot disagree about what one checkout is.
    const key = repoKey(workerId, gitToplevel)
    const existing = checkouts.get(key)
    if (!existing) {
      checkouts.set(key, {
        workerId,
        gitToplevel,
        repoKey: rk.key,
        repoLabel: rk.label,
        isWorktree: git.isWorktree === true,
        currentBranch: git.branchLabel ?? '',
        mru: tab.mru ?? -1,
        createdAt: tab.createdAt ?? '',
      })
      continue
    }
    // Merge: the checkout is as recent as its most recent tab, and any tab that
    // knows the branch or the worktree disposition supplies it.
    existing.mru = Math.max(existing.mru, tab.mru ?? -1)
    if ((tab.createdAt ?? '') > existing.createdAt)
      existing.createdAt = tab.createdAt ?? ''
    if (!existing.currentBranch && git.branchLabel)
      existing.currentBranch = git.branchLabel
    if (git.isWorktree !== undefined)
      existing.isWorktree = git.isWorktree
  }

  let entries = [...checkouts.values()]

  if (opts.isWorkerOnline)
    entries = entries.filter(c => opts.isWorkerOnline!(c.workerId))

  entries = dropRedundantWorktrees(entries)

  entries.sort(compareCheckouts)

  if (opts.limit !== undefined && entries.length > opts.limit)
    entries = entries.slice(0, opts.limit)

  return labelCheckouts(entries, opts.workerInfoFn)
}

/**
 * Drop each worktree whose own repository also has a normal checkout on the
 * same worker.
 *
 * Grouped by `(workerId, repoKey)` rather than by `repoKey` alone: the same
 * repository cloned on two machines is two places to start, and one machine's
 * main checkout says nothing about the other machine's worktrees.
 */
function dropRedundantWorktrees(entries: Checkout[]): Checkout[] {
  // NOT the store's `repoKey`: `c.repoKey` is a repository IDENTITY -- an
  // origin URL, or a local marker -- and not a toplevel, so that key space
  // would be a lie here.
  const hasMainCheckout = new Set<string>()
  for (const c of entries) {
    if (!c.isWorktree)
      hasMainCheckout.add(compositeKey(c.workerId, c.repoKey))
  }
  return entries.filter(c => !c.isWorktree || !hasMainCheckout.has(compositeKey(c.workerId, c.repoKey)))
}

/**
 * Most recently used first.
 *
 * `mru` is a monotonic COUNTER, not a timestamp (see `BaseTab.mru`): it orders
 * and nothing more, and a workspace whose tabs were never activated this
 * session carries none at all. Those fall to `createdAt`, then to the labels,
 * so the order is total and stable across renders.
 */
function compareCheckouts(a: Checkout, b: Checkout): number {
  if (a.mru !== b.mru)
    return b.mru - a.mru
  if (a.createdAt !== b.createdAt)
    return a.createdAt < b.createdAt ? 1 : -1
  if (a.repoLabel !== b.repoLabel)
    return a.repoLabel < b.repoLabel ? -1 : 1
  return a.gitToplevel < b.gitToplevel ? -1 : 1
}

/** Build each row's label, adding context only where it disambiguates. */
function labelCheckouts(
  entries: Checkout[],
  workerInfoFn?: (id: string) => WorkerInfo | null,
): RepoStartPoint[] {
  // The worker segment appears only once the list spans more than one worker.
  // On a solo desktop every row would otherwise carry the same prefix, which
  // says nothing and eats the width the repository name needs.
  const multiWorker = new Set(entries.map(c => c.workerId)).size > 1

  const baseLabel = (c: Checkout): string => {
    if (!multiWorker)
      return c.repoLabel
    const name = workerInfoFn?.(c.workerId)?.name || c.workerId
    return `${name} · ${c.repoLabel}`
  }

  const seen = new Map<string, number>()
  for (const c of entries) {
    const base = baseLabel(c)
    seen.set(base, (seen.get(base) ?? 0) + 1)
  }

  return entries.map((c) => {
    const base = baseLabel(c)
    // Two checkouts read the same: two clones of one repository on one worker,
    // or two unrelated repositories whose directories share a basename. The
    // path is the only thing that tells them apart.
    const label = (seen.get(base) ?? 0) > 1
      ? `${base} (${tildifyForWorker(c.gitToplevel, workerInfoFn?.(c.workerId))})`
      : base
    return {
      label,
      startPoint: {
        kind: 'repo',
        workerId: c.workerId,
        gitToplevel: c.gitToplevel,
        isWorktree: c.isWorktree,
        currentBranch: c.currentBranch || undefined,
      },
    }
  })
}
