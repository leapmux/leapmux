import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import { createMemo, createSignal } from 'solid-js'
import { createStore, produce, reconcile } from 'solid-js/store'
import * as workerRpc from '~/api/workerRpc'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { detectFlavor, relativeUnder, toPosixSeparators } from '~/lib/paths'

export type GitFilterTab = 'all' | 'changed' | 'staged' | 'unstaged'

export interface DiffStats { added: number, deleted: number, untracked: number }
const ZERO_DIFF_STATS: DiffStats = { added: 0, deleted: 0, untracked: 0 }

/**
 * Whether a git status entry names a whole untracked DIRECTORY rather than a
 * file. Git collapses an untracked directory into one `build/` entry, and the
 * trailing slash is the only marker -- it is git's own spelling, so it is `/`
 * on every platform regardless of the worker's path flavor.
 *
 * The one place that decodes this convention. `gitFileStatus.store` needs it to
 * build its prefix index and the tree filter needs it too;
 * decoding it twice is how the two came to disagree about the same rows.
 */
export function isUntrackedDirEntry(path: string): boolean {
  return path.endsWith('/')
}

/** Strips the trailing slash `isUntrackedDirEntry` matches on. */
export function untrackedDirBasePath(path: string): string {
  return isUntrackedDirEntry(path) ? path.slice(0, -1) : path
}

export function fileEntryToDiffStats(entry: GitFileStatusEntry): DiffStats {
  const isUntracked = entry.unstagedStatus === GitFileStatusCode.UNTRACKED
  return {
    added: isUntracked ? 0 : entry.linesAdded + entry.stagedLinesAdded,
    deleted: isUntracked ? 0 : entry.linesDeleted + entry.stagedLinesDeleted,
    untracked: isUntracked ? 1 : 0,
  }
}

/**
 * Adapts the `diff{Added,Deleted,Untracked}` field convention (tab store,
 * worktree-close prompts, etc.) to a DiffStats value.
 */
export function diffStatsFromTabFields(
  t: { diffAdded: number, diffDeleted: number, diffUntracked: number },
): DiffStats {
  return { added: t.diffAdded, deleted: t.diffDeleted, untracked: t.diffUntracked }
}

interface GitFileStatusState {
  isGitRepo: boolean
  /**
   * Worker the current status was read from.
   *
   * A repo is identified by `(workerId, toplevel)`, not by path alone — the
   * same absolute path is the normal case across two workers, not a
   * pathological one. `syncGitStatusToTabs` now stamps every workspace in the
   * account, so without this the branch label and diff badges of a repo on one
   * worker would be written onto identically-pathed tabs on another.
   */
  workerId: string
  repoRoot: string
  /**
   * Working-tree root of the queried path. Equal to `repoRoot` for a
   * main-tree query; the worktree dir for a worktree query. Separate
   * from `repoRoot` because the latter is canonical (used for
   * file-tree containment + worker file paths, which are relative to
   * the main repo even for worktree queries) while `toplevel` is what
   * `syncGitStatusToTabs` uses to match tabs — a worktree's tabs carry
   * `gitToplevel == toplevel` and main-tree tabs carry
   * `gitToplevel == repoRoot`. Stamping by `repoRoot` would have
   * smeared a worktree's branch across every main-tree tab whose
   * gitToplevel happened to equal repoRoot.
   */
  toplevel: string
  originUrl: string
  currentBranch: string
  /**
   * True when the focused dir resolves to a linked worktree (its
   * `--git-dir` differs from `--git-common-dir`). Mirrors onto every
   * tab under `toplevel` via syncGitStatusToTabs so the branch-row
   * context menu can hint at the worker without re-probing.
   */
  isWorktree: boolean
  /**
   * The worker's explanation when the queried path has no git status.
   *
   * `GetGitFileStatusResponse` reports "not a git repo" as a SUCCESSFUL empty
   * response carrying only this field — there is no `is_git_repo` flag on this
   * message — so it is the only signal distinguishing "not a repo" from "a repo
   * whose status we failed to read". Empty on every success.
   */
  errorHint: string
  files: GitFileStatusEntry[]
}

export function createGitFileStatusStore() {
  const [state, setState] = createStore<GitFileStatusState>({
    isGitRepo: false,
    workerId: '',
    repoRoot: '',
    toplevel: '',
    originUrl: '',
    currentBranch: '',
    errorHint: '',
    isWorktree: false,
    files: [],
  })

  const [loading, setLoading] = createSignal(false)

  // refresh() can fire from multiple unrelated paths (reactive workspace
  // refresh + AppShell's branch-change cross-repo refresh + sidebar
  // resync), and there's no createGuardedFetch-style owner here because
  // the store outlives every component that uses it. Without an in-
  // flight guard, two concurrent refresh() calls race their RPCs and
  // whichever response settles LAST overwrites the other's payload —
  // a problem when the late-settler is the OLDER request (worker swap
  // between the two refresh() calls, or a slow sibling repo's status
  // landing after a fresh-repo's). The setLoading(true)/(false) pair
  // is also non-monotonic — the first refresh's finally{} clears
  // loading=false while a second is still in flight. Generation
  // counter: each call bumps `gen`, captures its own `mine`, and only
  // applies its setState (and clears loading) when `mine === gen`.
  let gen = 0

  const refresh = async (workerId: string, path: string) => {
    if (!workerId || !path)
      return
    gen += 1
    const mine = gen
    setLoading(true)
    try {
      const resp = await workerRpc.getGitFileStatus(workerId, { workerId, path })
      if (mine !== gen)
        return
      // `toplevel` is authoritative — the worker sets it on every success
      // path (`git.go` queryGitPathInfo), and `git_test.go` pins
      // `toplevel == repo_root` for a main-tree query. Aliasing a missing
      // value onto `repo_root` would hand a WORKTREE tab the main tree's
      // root, which `isSameRepo` then matches as if it were a main-tree tab.
      const toplevel = resp.toplevel
      // "Not a git repo" arrives as a SUCCESSFUL empty response — the worker
      // returns `&GetGitFileStatusResponse{}` with only `errorHint` set when
      // `queryGitPathInfo` reports `errNotGitRepo`, and this message has no
      // `is_git_repo` field to say so directly (that one is on `GetGitInfo`).
      // So a resolved RPC is not evidence of a repo; a resolved toplevel is.
      // Flipping the flag on every reply left a plain directory advertising
      // `isGitRepo: true` with empty repoRoot/toplevel/branch, which renders
      // the git filter tab bar over a non-repo and discards the diagnostic
      // `errorHint` was added to surface.
      const isGitRepo = Boolean(toplevel)
      setState(produce((s) => {
        if (s.isGitRepo !== isGitRepo)
          s.isGitRepo = isGitRepo
        if (s.errorHint !== resp.errorHint)
          s.errorHint = resp.errorHint
        if (s.workerId !== workerId)
          s.workerId = workerId
        if (s.repoRoot !== resp.repoRoot)
          s.repoRoot = resp.repoRoot
        if (s.toplevel !== toplevel)
          s.toplevel = toplevel
        if (s.originUrl !== resp.originUrl)
          s.originUrl = resp.originUrl
        if (s.currentBranch !== resp.currentBranch)
          s.currentBranch = resp.currentBranch
        if (s.isWorktree !== resp.isWorktree)
          s.isWorktree = resp.isWorktree
      }))
      // Keyed reconcile, NOT a wholesale array replace -- the same rule
      // `setChildrenInStore` follows in DirectoryTree, and for the same two
      // reasons. Refresh fires every turn-end, so on a quiet repo `resp.files`
      // is a fresh array with identical contents: assigning it would rebuild
      // `filesByPath` and `prefixIndex` (which walks every file x every
      // ancestor), cascade every TreeNode's diffStats memo, and hand `<For>`
      // fresh objects, which disposes and re-creates every flat-list row. Now
      // that the sidebar SORTS by size and modTime, a plain edit that leaves the
      // line counts alone changes those fields on one entry -- so the old
      // field-by-field guard missed its fast path on any turn that wrote a
      // single byte. Reconciling by `path` writes only the fields that really
      // differ, on only the entries that really differ, so an unrelated file's
      // mtime no longer repaints the list.
      //
      // This also retires the hand-maintained field list the guard carried: a
      // field added to the proto and forgotten there used to pin the stale
      // value on screen. Reconcile compares every field by construction.
      setState('files', reconcile(resp.files, { key: 'path' }))
    }
    catch {
      if (mine !== gen)
        return
      // Mirror the success-path guard: skip writes when each field is
      // already at its zero value so consecutive failures (e.g. a flaky
      // probe during connection blips) don't re-fire reactive memos for
      // an unchanged reset.
      setState(produce((s) => {
        if (s.isGitRepo)
          s.isGitRepo = false
        if (s.errorHint !== '')
          s.errorHint = ''
        if (s.workerId !== '')
          s.workerId = ''
        if (s.repoRoot !== '')
          s.repoRoot = ''
        if (s.toplevel !== '')
          s.toplevel = ''
        if (s.originUrl !== '')
          s.originUrl = ''
        if (s.currentBranch !== '')
          s.currentBranch = ''
        if (s.isWorktree)
          s.isWorktree = false
        if (s.files.length !== 0)
          s.files = []
      }))
    }
    finally {
      // Only the latest call clears loading. A stale call returning
      // first must NOT flip loading=false while the latest is still in
      // flight — that would make the spinner disappear prematurely.
      if (mine === gen)
        setLoading(false)
    }
  }

  // Resets every field, `errorHint` included. The catch path above already
  // clears it, and leaving it behind here kept the previous directory's
  // diagnostic beside an otherwise empty store.
  const clear = () => {
    setState({ isGitRepo: false, workerId: '', repoRoot: '', toplevel: '', originUrl: '', currentBranch: '', errorHint: '', isWorktree: false, files: [] })
  }

  /**
   * The root every status path in `state.files` is relative to.
   *
   * `toplevel`, NOT `repoRoot`: `git status --porcelain=v2` emits paths
   * relative to the WORKING-TREE root, and in a linked worktree `repoRoot` is
   * the parent repo instead (`parseGitPathInfoOutput` rewrites it to
   * `dirname(--git-common-dir)`). A worktree lives at
   * `<repo-parent>/<repo>-worktrees/<branch>`, a SIBLING of the repo, so
   * `relativeUnder` against `repoRoot` returns null for every row -- which
   * blanked the diff badges, the file-status icons and the git filter tabs on
   * every worktree tab. `repoRoot` stays the identity the sidebar groups by;
   * it is not a path base. Falls back only when `toplevel` is empty, which is
   * the "not a git repo" reply that carries no files either.
   */
  const statusRoot = () => state.toplevel || state.repoRoot

  // Memoized so the regex runs once per root change, not once per
  // TreeNode's hasChanges/getFileStatus/getDirDiffStats call.
  const rootFlavor = createMemo(() => detectFlavor(statusRoot()))

  // Relativize a flavor-native absolute path to a git-style (posix-separated)
  // path under the working-tree root, or null if it isn't under that tree.
  const relToRepo = (absPath: string): string | null => {
    const root = statusRoot()
    if (!root)
      return null
    const flavor = rootFlavor()
    const rel = relativeUnder(absPath, root, flavor)
    if (rel === null)
      return null
    return flavor === 'posix' ? rel : toPosixSeparators(rel)
  }

  // O(1) lookup by relative path. Reads only `path`, so the keyed reconcile in
  // refresh() keeps this memo from re-running when a file's size or mtime moves
  // but the entry set does not.
  const filesByPath = createMemo(() => {
    const m = new Map<string, GitFileStatusEntry>()
    for (const f of state.files)
      m.set(f.path, f)
    return m
  })

  const getFileStatus = (absPath: string): GitFileStatusEntry | undefined => {
    const rel = relToRepo(absPath)
    if (rel === null)
      return undefined
    return filesByPath().get(rel)
  }

  const getChangedFiles = (filter: GitFilterTab): GitFileStatusEntry[] => {
    if (filter === 'all')
      return state.files
    return state.files.filter((f) => {
      if (filter === 'staged') {
        return f.stagedStatus !== GitFileStatusCode.UNSPECIFIED
      }
      if (filter === 'unstaged') {
        return f.unstagedStatus !== GitFileStatusCode.UNSPECIFIED
      }
      // 'changed' — any change (staged or unstaged)
      return f.stagedStatus !== GitFileStatusCode.UNSPECIFIED
        || f.unstagedStatus !== GitFileStatusCode.UNSPECIFIED
    })
  }

  // Git emits "build/" when an entire subtree is untracked; those entries
  // implicitly cover any descendant path, which we can't pre-populate without
  // knowing queries, so we track them separately and check at lookup time.
  const prefixIndex = createMemo(() => {
    const prefixStats = new Map<string, DiffStats>()
    const untrackedDirSet = new Set<string>()
    // Per-prefixIndex-generation cache of merged dir stats. Returning the
    // same object reference across calls keeps downstream `createMemo`s
    // (one per TreeNode row) stable across no-op refreshes — without it,
    // any row whose ancestor is in `untrackedDirSet` re-invalidates every
    // refresh because `lookupDirStats` allocated a fresh object.
    const dirStatsCache = new Map<string, DiffStats>()

    const bump = (key: string, f: GitFileStatusEntry, isUntracked: boolean) => {
      let s = prefixStats.get(key)
      if (!s) {
        s = { added: 0, deleted: 0, untracked: 0 }
        prefixStats.set(key, s)
      }
      if (isUntracked) {
        s.untracked++
      }
      else {
        s.added += f.linesAdded + f.stagedLinesAdded
        s.deleted += f.linesDeleted + f.stagedLinesDeleted
      }
    }

    for (const f of state.files) {
      const isUntracked = f.unstagedStatus === GitFileStatusCode.UNTRACKED
      const isDirEntry = isUntrackedDirEntry(f.path)
      const basePath = untrackedDirBasePath(f.path)
      if (isDirEntry)
        untrackedDirSet.add(basePath)
      bump('', f, isUntracked)
      let i = 0
      while (i < basePath.length) {
        const next = basePath.indexOf('/', i)
        if (next === -1) {
          bump(basePath, f, isUntracked)
          break
        }
        bump(basePath.slice(0, next), f, isUntracked)
        i = next + 1
      }
    }
    return { prefixStats, untrackedDirSet, dirStatsCache }
  })

  // An untracked "build/" also covers descendants like "build/bin"; the
  // ancestor/self case is already in prefixStats. Walks `relDir`'s
  // ancestor segments and probes the set — O(depth) per node instead of
  // O(untrackedDirs) per node.
  const untrackedAncestorMatches = (relDir: string, untrackedDirSet: Set<string>): number => {
    if (untrackedDirSet.size === 0)
      return 0
    let n = 0
    let i = relDir.lastIndexOf('/')
    while (i > 0) {
      if (untrackedDirSet.has(relDir.slice(0, i)))
        n++
      i = relDir.lastIndexOf('/', i - 1)
    }
    return n
  }

  const lookupDirStats = (relDir: string): DiffStats => {
    const { prefixStats, untrackedDirSet, dirStatsCache } = prefixIndex()
    const cached = dirStatsCache.get(relDir)
    if (cached)
      return cached
    const base = prefixStats.get(relDir) ?? ZERO_DIFF_STATS
    const extraUntracked = untrackedAncestorMatches(relDir, untrackedDirSet)
    const result = extraUntracked === 0
      ? base
      : { added: base.added, deleted: base.deleted, untracked: base.untracked + extraUntracked }
    dirStatsCache.set(relDir, result)
    return result
  }

  const getNodeDiffStats = (absPath: string, isDir: boolean): DiffStats => {
    if (isDir) {
      const relDir = relToRepo(absPath)
      return relDir === null ? ZERO_DIFF_STATS : lookupDirStats(relDir)
    }
    const entry = getFileStatus(absPath)
    return entry ? fileEntryToDiffStats(entry) : ZERO_DIFF_STATS
  }

  const hasChanges = (dirPath: string): boolean => {
    const relDir = relToRepo(dirPath)
    if (relDir === null)
      return false
    const { prefixStats, untrackedDirSet } = prefixIndex()
    return prefixStats.has(relDir) || untrackedAncestorMatches(relDir, untrackedDirSet) > 0
  }

  return {
    state,
    loading,
    refresh,
    clear,
    statusRoot,
    getFileStatus,
    getChangedFiles,
    getNodeDiffStats,
    hasChanges,
  }
}
