import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import { createMemo, createSignal } from 'solid-js'
import { createStore, produce } from 'solid-js/store'
import * as workerRpc from '~/api/workerRpc'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { detectFlavor, relativeUnder, toPosixSeparators } from '~/lib/paths'

export { GitFileStatusCode }
export type { GitFileStatusEntry }

export type GitFilterTab = 'all' | 'changed' | 'staged' | 'unstaged'

export interface DiffStats { added: number, deleted: number, untracked: number }
const ZERO_DIFF_STATS: DiffStats = { added: 0, deleted: 0, untracked: 0 }

export function fileEntryToDiffStats(entry: GitFileStatusEntry): DiffStats {
  const isUntracked = entry.unstagedStatus === GitFileStatusCode.UNTRACKED
  return {
    added: isUntracked ? 0 : entry.linesAdded + entry.stagedLinesAdded,
    deleted: isUntracked ? 0 : entry.linesDeleted + entry.stagedLinesDeleted,
    untracked: isUntracked ? 1 : 0,
  }
}

/** Adapts the `diff{Added,Deleted,Untracked}` field convention (tab store,
 * worktree-close prompts, etc.) to a DiffStats value. */
export function diffStatsFromTabFields(
  t: { diffAdded: number, diffDeleted: number, diffUntracked: number },
): DiffStats {
  return { added: t.diffAdded, deleted: t.diffDeleted, untracked: t.diffUntracked }
}

// Refresh fires every turn-end. On a quiet repo, resp.files is a fresh
// array with identical contents; reassigning would invalidate prefixIndex
// (walks every file × every ancestor), cascade every TreeNode's diffStats
// memo, and repaint unchanged rows.
function sameFileEntries(a: readonly GitFileStatusEntry[], b: readonly GitFileStatusEntry[]): boolean {
  if (a === b)
    return true
  if (a.length !== b.length)
    return false
  for (let i = 0; i < a.length; i++) {
    const x = a[i]
    const y = b[i]
    if (x === y)
      continue
    if (x.path !== y.path
      || x.stagedStatus !== y.stagedStatus
      || x.unstagedStatus !== y.unstagedStatus
      || x.linesAdded !== y.linesAdded
      || x.linesDeleted !== y.linesDeleted
      || x.stagedLinesAdded !== y.stagedLinesAdded
      || x.stagedLinesDeleted !== y.stagedLinesDeleted
      || x.oldPath !== y.oldPath) {
      return false
    }
  }
  return true
}

interface GitFileStatusState {
  isGitRepo: boolean
  repoRoot: string
  originUrl: string
  currentBranch: string
  files: GitFileStatusEntry[]
}

export function createGitFileStatusStore() {
  const [state, setState] = createStore<GitFileStatusState>({
    isGitRepo: false,
    repoRoot: '',
    originUrl: '',
    currentBranch: '',
    files: [],
  })

  const [loading, setLoading] = createSignal(false)

  const refresh = async (workerId: string, path: string) => {
    if (!workerId || !path)
      return
    setLoading(true)
    try {
      const resp = await workerRpc.getGitFileStatus(workerId, { workerId, path })
      setState(produce((s) => {
        if (!s.isGitRepo)
          s.isGitRepo = true
        if (s.repoRoot !== resp.repoRoot)
          s.repoRoot = resp.repoRoot
        if (s.originUrl !== resp.originUrl)
          s.originUrl = resp.originUrl
        if (s.currentBranch !== resp.currentBranch)
          s.currentBranch = resp.currentBranch
        // Preserve the existing reference when the file list is unchanged so
        // the prefixIndex memo (and any downstream signals) don't rebuild.
        if (!sameFileEntries(s.files, resp.files))
          s.files = resp.files
      }))
    }
    catch {
      setState(produce((s) => {
        s.isGitRepo = false
        s.repoRoot = ''
        s.originUrl = ''
        s.currentBranch = ''
        s.files = []
      }))
    }
    finally {
      setLoading(false)
    }
  }

  const clear = () => {
    setState({ isGitRepo: false, repoRoot: '', originUrl: '', currentBranch: '', files: [] })
  }

  // Memoized so the regex runs once per repoRoot change, not once per
  // TreeNode's hasChanges/getFileStatus/getDirDiffStats call.
  const rootFlavor = createMemo(() => detectFlavor(state.repoRoot))

  // Relativize a flavor-native absolute path to a git-style (posix-separated)
  // path under repoRoot, or null if it isn't under the repo.
  const relToRepo = (absPath: string): string | null => {
    const root = state.repoRoot
    if (!root)
      return null
    const flavor = rootFlavor()
    const rel = relativeUnder(absPath, root, flavor)
    if (rel === null)
      return null
    return flavor === 'posix' ? rel : toPosixSeparators(rel)
  }

  const getFileStatus = (absPath: string): GitFileStatusEntry | undefined => {
    const rel = relToRepo(absPath)
    if (rel === null)
      return undefined
    return state.files.find(f => f.path === rel)
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
    const untrackedDirs: string[] = []

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
      const isDirEntry = f.path.endsWith('/')
      const basePath = isDirEntry ? f.path.slice(0, -1) : f.path
      if (isDirEntry)
        untrackedDirs.push(basePath)
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
    return { prefixStats, untrackedDirs }
  })

  // An untracked "build/" also covers descendants like "build/bin"; the
  // ancestor/self case is already in prefixStats.
  const untrackedAncestorMatches = (relDir: string, untrackedDirs: string[]): number => {
    let n = 0
    for (const d of untrackedDirs) {
      if (relDir.length > d.length && relDir.startsWith(`${d}/`))
        n++
    }
    return n
  }

  const lookupDirStats = (relDir: string): DiffStats => {
    const { prefixStats, untrackedDirs } = prefixIndex()
    const base = prefixStats.get(relDir) ?? ZERO_DIFF_STATS
    const extraUntracked = untrackedAncestorMatches(relDir, untrackedDirs)
    if (extraUntracked === 0)
      return base
    return { added: base.added, deleted: base.deleted, untracked: base.untracked + extraUntracked }
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
    const { prefixStats, untrackedDirs } = prefixIndex()
    return prefixStats.has(relDir) || untrackedAncestorMatches(relDir, untrackedDirs) > 0
  }

  return {
    state,
    loading,
    refresh,
    clear,
    getFileStatus,
    getChangedFiles,
    getNodeDiffStats,
    hasChanges,
  }
}
