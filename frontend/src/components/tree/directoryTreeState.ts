import type { FileSortFields, FileSortKey } from '~/lib/fileSort'
import type { PathFlavor } from '~/lib/paths'
import { normalizeSeparators, pathEq, relativeUnder } from '~/lib/paths'

// The tree's own state: the row it caches, the schema it persists, and the
// pure predicates its rendering asks. None of this reads a store or a prop, so
// it is testable without mounting a component -- which is why it lives beside
// `DirectoryTree` rather than inside it.

export interface TreeNodeData {
  path: string
  displayName: string
  isDir: boolean
  hidden: boolean
  /**
   * Bytes, from the listing's stat. For a DIRECTORY this is the inode's own
   * size, which says nothing about its contents — nothing displays or sorts on
   * it (see `makeFileComparator` and `FileActionsMenu`'s `showSize`).
   */
  size: number
  /** RFC3339 UTC, from the listing's stat. */
  modTime: string
}

/** The fields the sort comparator reads off a tree node. */
export const TREE_SORT_FIELDS: FileSortFields<TreeNodeData> = {
  name: node => node.displayName,
  isDir: node => node.isDir,
  size: node => node.size,
  modTime: node => node.modTime || undefined,
}

// Content equality for the children cache; see setChildrenInStore.
export function sameTreeEntries(a: readonly TreeNodeData[], b: readonly TreeNodeData[]): boolean {
  if (a === b)
    return true
  if (a.length !== b.length)
    return false
  for (let i = 0; i < a.length; i++) {
    const x = a[i]
    const y = b[i]
    // size and modTime are part of the comparison because the tree SORTS and
    // DISPLAYS them: without them, a file whose contents changed but whose
    // name did not would keep its stale size in the three-dot menu and its
    // stale position under a size or modified sort. It does cost fast-path
    // hits that the pre-sort tree never lost -- see setChildrenInStore.
    if (x.path !== y.path || x.displayName !== y.displayName || x.isDir !== y.isDir
      || x.hidden !== y.hidden || x.size !== y.size || x.modTime !== y.modTime) {
      return false
    }
  }
  return true
}

// -------------------------------------------------------------------------
// Serialization helpers for sessionStorage
// -------------------------------------------------------------------------

/**
 * Schema version for the persisted tree state.
 *
 * Bump this whenever the SHAPE of anything in the payload changes -- a field
 * added to or removed from `TreeNodeData`, or a change to how the two path maps
 * are keyed. The next load then discards the whole payload and re-fetches,
 * instead of hydrating a shape the current code misreads.
 *
 * A version, not a per-field probe: the probe this replaced tested two of
 * `TreeNodeData`'s six fields and could not reach `expandedPaths` or
 * `truncatedDirs` at all, so the next field added here would have needed
 * someone to remember to extend it. Same mechanism as
 * `STORED_ROW_HEIGHTS_VERSION` in the chat's row-height cache.
 *
 * 1: entries carry `size` and `modTime`, so the sidebar can sort by them.
 */
export const DIRECTORY_TREE_STATE_VERSION = 1

export interface DirectoryTreeStateJSON {
  v?: number
  expandedPaths: Record<string, boolean>
  childrenCache: Record<string, TreeNodeData[]>
  truncatedDirs?: Record<string, number>
}

/**
 * The stored shape, as an OBJECT.
 *
 * Not a string: `sessionStorageSet` serializes whatever it is handed, so a
 * pre-stringified payload is stringified a second time -- once to build it,
 * once to escape every quotation mark of it inside the wrapper. This effect
 * runs on every store write, and a `/`-rooted tree caches many more directory
 * listings than a home-rooted one did.
 */
export function serializeState(
  expandedPaths: Record<string, boolean>,
  childrenCache: Record<string, TreeNodeData[]>,
  truncatedDirs: Record<string, number>,
): DirectoryTreeStateJSON {
  return { v: DIRECTORY_TREE_STATE_VERSION, expandedPaths, childrenCache, truncatedDirs }
}

/**
 * Restores the persisted tree state, or null when the payload is unusable.
 *
 * A version mismatch discards EVERYTHING, expansion included. That costs a
 * collapsed tree once per bump, and it buys the one rule that covers every key
 * in the payload: a partial restore would have to prove, for each surviving
 * key, that the old shape still reads correctly under the new code.
 */
export function deserializeState(json: DirectoryTreeStateJSON | undefined): { expandedPaths: Record<string, boolean>, childrenCache: Record<string, TreeNodeData[]>, truncatedDirs: Record<string, number> } | null {
  try {
    if (!json || typeof json !== 'object' || json.v !== DIRECTORY_TREE_STATE_VERSION)
      return null
    return {
      expandedPaths: json.expandedPaths ?? {},
      // Still filtered, for a payload that is the right VERSION but corrupt --
      // a hand edit, or a truncated write. The version answers "is this shape
      // current"; this answers "is this value well formed".
      childrenCache: wellFormedCachedChildren(json.childrenCache),
      truncatedDirs: json.truncatedDirs ?? {},
    }
  }
  catch {
    return null
  }
}

/** Drops any cached directory whose entries are not a well-formed array. */
export function wellFormedCachedChildren(cache: Record<string, TreeNodeData[]> | undefined): Record<string, TreeNodeData[]> {
  const usable: Record<string, TreeNodeData[]> = {}
  for (const [path, entries] of Object.entries(cache ?? {})) {
    if (!Array.isArray(entries))
      continue
    if (entries.every(e => typeof e?.path === 'string' && typeof e?.displayName === 'string'))
      usable[path] = entries
  }
  return usable
}

// -------------------------------------------------------------------------
// Visibility helpers
// -------------------------------------------------------------------------

// `relativeUnder` requires both inputs to already use the flavor's separator,
// and nothing upstream guarantees that: `PathInput` submits whatever the user
// typed, so a win32 selection reaches the tree spelled `C:/Users/alice` while
// the worker's own listings spell it `C:\Users\alice`. Comparing the two
// answers "not under", which collapses the whole reveal -- no chain request,
// no node expanded, no row selected. Normalize at the comparison, the way
// `relativizePath` already does.
export function isDescendantPath(child: string, parent: string, flavor: PathFlavor): boolean {
  const rel = relativeUnder(normalizeSeparators(child, flavor), normalizeSeparators(parent, flavor), flavor)
  return rel !== null && rel !== ''
}

// Whether two paths address the same node, under the same normalization.
//
// `===` is wrong on win32 twice over: the worker's listing spells a name in
// its own case, and a typed path can use either separator. A node the tree
// already holds then looks like one it has never seen.
export function samePath(a: string, b: string, flavor: PathFlavor): boolean {
  return pathEq(normalizeSeparators(a, flavor), normalizeSeparators(b, flavor), flavor)
}

/**
 * The inline row shown under a directory the worker truncated.
 *
 * The worker sorts by name and cuts at its entry limit BEFORE stat-ing, so a
 * sort by anything else orders only the entries that survived that cut. The
 * notice states the window, so the user does not read a partial answer as a
 * complete one.
 */
export function formatTruncationNotice(shown: number, total: number, sortKey: FileSortKey): string {
  // The worker reports what the directory really held, so the notice gives the
  // size of what is hidden instead of only that something is. `total` is 0 for
  // a listing restored from a cache written before the worker sent it.
  const count = total > shown ? `${shown} of ${total} entries` : `${shown}+ entries`
  return sortKey === 'name'
    ? `${count}, listing truncated`
    : `${count}, truncated by name before sorting`
}

/** The rows one directory renders: hidden/git filters first, then the sort. */
export function visibleSortedChildren(
  all: readonly TreeNodeData[],
  showHidden: boolean,
  isVisible: ((path: string) => boolean) | undefined,
  comparator: (a: TreeNodeData, b: TreeNodeData) => number,
): TreeNodeData[] {
  const filtered = showHidden && !isVisible
    ? all
    : all.filter(c => (showHidden || !c.hidden) && (!isVisible || isVisible(c.path)))
  return filtered.toSorted(comparator)
}
