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

import type { RepoGitStore, RepoGitView } from '~/stores/repoGit'
import type { Tab } from '~/stores/tab.types'
import { basename } from '~/lib/paths'
import { repoGitView } from '~/stores/repoGit'

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
 * Repo toplevel for structural keys (branch buckets, delete-branch tab sets)
 * and for the sidebar's repository grouping. The ONE place that answers "which
 * repository is this tab in", so a caller cannot re-add the row fallback below.
 *
 * `repoGitView` already resolves the two sources correctly, and this trusts it:
 *
 *  - No store entry yet: the view returns the row's `gitToplevel`, so a tab
 *    stays grouped from the moment it is opened, before any probe lands.
 *  - An entry that resolved a repo: the view returns the store's toplevel,
 *    which wins over tab metadata that can lag on probe-path orphans and
 *    subdir agents.
 *  - An entry that says "not a git repository": the view returns undefined,
 *    and so does this. That answer came FROM the worker, so a stale row value
 *    must not override it -- doing so filed the tab under a repository that
 *    does not exist there, with no branch name, until the page reloaded.
 *
 * The row IS the fallback when no store key resolves at all, which happens for
 * a tab with no `workerId`. There is no entry to believe or disbelieve then,
 * and the row is the only thing anyone knows.
 */
export function tabGitToplevelForKey(
  tab: Pick<Tab, 'workerId' | 'gitToplevel' | 'workingDir'>,
  store: RepoGitStore,
  // Callers that already resolved the view pass it; the default resolves it,
  // so a one-off caller keeps the two-argument form. Resolving twice per call
  // was the sidebar's hottest redundant work -- each resolution may run the
  // canonical-repo-key scan for a toplevel-less tab.
  git: RepoGitView = repoGitView(tab, store),
): string {
  if (!git.key)
    return tab.gitToplevel ?? ''
  return git.toplevel ?? ''
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
export function tabBranchKey(
  tab: Pick<Tab, 'workerId' | 'gitToplevel' | 'workingDir'>,
  store: RepoGitStore,
  git: RepoGitView = repoGitView(tab, store),
): string {
  return branchKey(git.branchLabel || null, tab.workerId ?? '', tabGitToplevelForKey(tab, store, git))
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

const SSH_ORIGIN_RE = /^git@([^:]+):(.+)$/
const PROTOCOL_PREFIX_RE = /^https?:\/\//
const TRAILING_DOT_GIT_RE = /\.git$/
const TRAILING_SLASH_RE = /\/$/

/**
 * An origin URL as a person reads it: `git@github.com:org/repo.git` becomes
 * `github.com/org/repo`.
 *
 * Display only. The KEY is always the raw origin URL, so two spellings of one
 * remote stay two repos rather than merging under a prettified name.
 */
export function formatGitOriginUrl(url: string): string {
  if (!url)
    return ''
  let result = url
  // Convert SSH format: git@github.com:org/repo -> github.com/org/repo
  const sshMatch = result.match(SSH_ORIGIN_RE)
  if (sshMatch)
    result = `${sshMatch[1]}/${sshMatch[2]}`
  // Strip protocols
  result = result.replace(PROTOCOL_PREFIX_RE, '')
  // Strip trailing .git
  result = result.replace(TRAILING_DOT_GIT_RE, '')
  // Strip trailing slash
  result = result.replace(TRAILING_SLASH_RE, '')
  return result
}

/**
 * The grouping key and the display label for a tab's repository. Order of
 * precedence:
 *   1. gitOriginUrl -- a remote we can format nicely.
 *   2. gitToplevel -- an origin-less local repo; the toplevel path makes
 *      distinct repos distinct.
 * A tab that has neither answers null, and its caller buckets it as ungrouped.
 *
 * It lives HERE, beside `branchKey` and `repoKeyForLocal`, because it is the
 * repo half of the same composite-key rule: the sidebar tree groups by it, and
 * the section header menu lists the repos a section works on by it. Two copies
 * would let those two disagree about which checkouts are one repository.
 *
 * NOT the same `repoKey` as `~/stores/repoGit`, which keys the git store by
 * `(workerId, gitToplevel)`. This one keys by REPO IDENTITY, so two clones of
 * one origin on two workers share it.
 */
export function repoKeyAndLabel(
  tab: Pick<Tab, 'workerId' | 'gitToplevel' | 'workingDir'>,
  store: RepoGitStore,
  git: RepoGitView = repoGitView(tab, store),
): { key: string, label: string } | null {
  if (git.originUrl)
    return { key: git.originUrl, label: formatGitOriginUrl(git.originUrl) }
  // Through the shared helper, so the group key and the branch bucket resolve
  // the toplevel the same way. A second copy of the chain here is what let a
  // stale row value override the worker's "not a git repository" answer. The
  // view resolved once at the top carries through.
  const toplevel = tabGitToplevelForKey(tab, store, git)
  if (toplevel) {
    const label = basename(toplevel) || toplevel
    return { key: repoKeyForLocal(toplevel), label }
  }
  return null
}

/**
 * Join `parts` into one key with the separator this module owns.
 *
 * Exported so a caller that needs a composite of its own does not concatenate a
 * control byte inline, which is the rule this module's own doc states.
 */
export function compositeKey(...parts: string[]): string {
  return parts.join(KEY_SEP)
}

/** Composite key for the per-row collapse state (repo + branch). */
export function collapseKeyForBranch(repoKey: string, branchKey: string): string {
  return compositeKey(repoKey, branchKey)
}
