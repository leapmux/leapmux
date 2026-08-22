import type { GitFilterTab, RepoGitState, RepoKey } from './repoGit'
import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import { createMemo, createSignal } from 'solid-js'
import { createStore, produce, reconcile } from 'solid-js/store'
import * as workerRpc from '~/api/workerRpc'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { detectFlavor, relativeUnder, toPosixSeparators } from '~/lib/paths'
import {
  fileEntryToDiffStats,
  isUntrackedDirEntry,
  patchFromGetGitFileStatus,
  untrackedDirBasePath,
} from './repoGit'

export type { DiffStats, GitFilterTab } from './repoGit'
export {
  aggregateDiffStats,
  diffStatsFromRepo,
  fileEntryToDiffStats,
  isUntrackedDirEntry,
  untrackedDirBasePath,
} from './repoGit'

const ZERO_DIFF_STATS = { added: 0, deleted: 0, untracked: 0 }

function emptyRepoState(): RepoGitState {
  return {
    workerId: '',
    repoRoot: '',
    toplevel: '',
    branch: '',
    originUrl: '',
    isWorktree: false,
    ahead: 0,
    behind: 0,
    conflicted: false,
    stashed: false,
    deleted: false,
    renamed: false,
    modified: false,
    typeChanged: false,
    added: false,
    untracked: false,
    diffAdded: 0,
    diffDeleted: 0,
    diffUntracked: 0,
    files: [],
    errorHint: '',
  }
}

export function createRepoGitStore() {
  const [repos, setRepos] = createStore<Record<RepoKey, RepoGitState>>({})
  const [focusedKey, setFocusedKeySignal] = createSignal<RepoKey | undefined>(undefined)
  const [loading, setLoading] = createSignal(false)

  let gen = 0

  const get = (key: RepoKey): RepoGitState | undefined => repos[key]

  const upsert = (key: RepoKey, patch: Partial<RepoGitState>) => {
    const { files, ...rest } = patch
    setRepos(produce((map) => {
      const prev = map[key] ?? emptyRepoState()
      map[key] = { ...prev, ...rest }
    }))
    if (files)
      setRepos(key, 'files', reconcile(files, { key: 'path' }))
  }

  const clear = (key: RepoKey) => {
    setRepos(produce((map) => {
      delete map[key]
    }))
  }

  const clearAll = () => {
    setRepos({})
  }

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
      const mapped = patchFromGetGitFileStatus(workerId, resp)
      if (!mapped) {
        if (mine === gen)
          setLoading(false)
        return
      }
      upsert(mapped.key, mapped.patch)
    }
    catch {
      if (mine !== gen)
        return
      const key = focusedKey()
      if (key)
        upsert(key, emptyRepoState())
    }
    finally {
      if (mine === gen)
        setLoading(false)
    }
  }

  const focusedState = createMemo(() => {
    const key = focusedKey()
    return key ? repos[key] : undefined
  })

  const statusRoot = createMemo(() => {
    const s = focusedState()
    return s?.toplevel || s?.repoRoot || ''
  })

  const rootFlavor = createMemo(() => detectFlavor(statusRoot()))

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

  const filesByPath = createMemo(() => {
    const m = new Map<string, GitFileStatusEntry>()
    for (const f of focusedState()?.files ?? [])
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
    const files = focusedState()?.files ?? []
    if (filter === 'all')
      return files
    return files.filter((f) => {
      if (filter === 'staged')
        return f.stagedStatus !== GitFileStatusCode.UNSPECIFIED
      if (filter === 'unstaged')
        return f.unstagedStatus !== GitFileStatusCode.UNSPECIFIED
      return f.stagedStatus !== GitFileStatusCode.UNSPECIFIED
        || f.unstagedStatus !== GitFileStatusCode.UNSPECIFIED
    })
  }

  const prefixIndex = createMemo(() => {
    const prefixStats = new Map<string, { added: number, deleted: number, untracked: number }>()
    const untrackedDirSet = new Set<string>()
    const dirStatsCache = new Map<string, { added: number, deleted: number, untracked: number }>()

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

    for (const f of focusedState()?.files ?? []) {
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

  const lookupDirStats = (relDir: string) => {
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

  const getNodeDiffStats = (absPath: string, isDir: boolean) => {
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
    get,
    upsert,
    clear,
    clearAll,
    repos: () => repos as Readonly<Record<RepoKey, RepoGitState>>,
    focusedKey,
    setFocusedKey: setFocusedKeySignal,
    refresh,
    loading,
    focusedState,
    statusRoot,
    getFileStatus,
    getChangedFiles,
    getNodeDiffStats,
    hasChanges,
  }
}
