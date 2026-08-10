/**
 * Composite-key plumbing for the workspace tab tree.
 *
 * The tree groups tabs by (repo, branch) where each axis allows inputs
 * that could otherwise collide under a naive `${a}:${b}` join:
 *
 *   - Branch names cannot contain control bytes, but the "no branch"
 *     bucket needs a key that can't collide with a real branch literally
 *     named "(no branch)".
 *   - Local-only repos (no origin URL) key off their toplevel path; the
 *     null-byte prefix distinguishes them from any real origin URL (git
 *     origin URLs cannot begin with a null byte).
 *   - The composite branch key joins (branchName, workerId, gitToplevel)
 *     so two clones of the same repo on the same branch stay separate.
 *
 * Encapsulating the bytes here keeps the rendering call sites
 * declarative — callers invoke `branchKey(...)` / `repoKeyForLocal(...)`
 * rather than concatenating control bytes inline.
 */

import type { Tab } from '~/stores/tab.types'

const KEY_SEP = '\x00'
const NO_BRANCH_NAME_SEGMENT = '\x02'
const LOCAL_PREFIX = '\x00local:'

/**
 * Branch-name-only key for in-repo collision counting (label
 * disambiguation). `null` maps to a sentinel that cannot collide with
 * any real branch name.
 */
export function branchNameSegment(branchName: string | null): string {
  return branchName === null ? NO_BRANCH_NAME_SEGMENT : branchName
}

/**
 * Key for a (branchName, workerId, gitToplevel) tuple. `branchName` may
 * be null to represent the "no branch" bucket.
 */
export function branchKey(branchName: string | null, workerId: string, gitToplevel: string): string {
  return `${branchNameSegment(branchName)}${KEY_SEP}${workerId}${KEY_SEP}${gitToplevel}`
}

/**
 * The branch group a tab belongs to.
 *
 * Every surface that answers "which tabs are on this branch" must use this one
 * function: the sidebar groups its tree by it, and the composer's branch chip
 * collects the tab list it hands to the delete-branch dialog by it. A second
 * membership test would let the dialog report a different set of affected tabs
 * than the tree shows.
 *
 * The parameter is a `Pick` of the real tab, not a structural shape with three
 * optional fields. Every field being optional made a `BranchGroup` -- which
 * carries `branchName`, not `gitBranch` -- a valid argument that silently
 * returned the "(no branch)" key, which is exactly the drift above.
 */
export function tabBranchKey(tab: Pick<Tab, 'gitBranch' | 'workerId' | 'gitToplevel'>): string {
  return branchKey(tab.gitBranch || null, tab.workerId ?? '', tab.gitToplevel ?? '')
}

/** Repo key for an origin-less local repo, identified by its toplevel. */
export function repoKeyForLocal(toplevel: string): string {
  return `${LOCAL_PREFIX}${toplevel}`
}

/** True iff the key was minted via {@link repoKeyForLocal}. */
export function isLocalRepoKey(key: string): boolean {
  return key.startsWith(LOCAL_PREFIX)
}

/**
 * Returns the human-readable identifier behind a repo key — the toplevel
 * path for local repos, the origin URL itself otherwise. Used as the
 * tooltip on the repo group header.
 */
export function repoKeyTooltip(key: string): string {
  return isLocalRepoKey(key) ? key.slice(LOCAL_PREFIX.length) : key
}

/** Composite key for the per-row collapse state (repo + branch). */
export function collapseKeyForBranch(repoKey: string, branchKey: string): string {
  return `${repoKey}${KEY_SEP}${branchKey}`
}
