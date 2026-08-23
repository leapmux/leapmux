import type { GitFilterTab, RepoGitRefreshOpts, RepoGitState, RepoKey } from './repoGit'
import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import { createMemo, createSignal } from 'solid-js'
import { createStore, produce, reconcile } from 'solid-js/store'
import * as workerRpc from '~/api/workerRpc'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { createLogger } from '~/lib/logger'
import { detectFlavor, relativeUnder, toPosixSeparators } from '~/lib/paths'
import {
  applyFullGitStatusUpsert,
  fileEntryToDiffStats,
  findCanonicalRepoKey,
  hasHealthyRepoForProbe,
  isUntrackedDirEntry,
  patchFromGetGitFileStatus,
  patchFromNonRepoGetGitFileStatus,
  repoKey,
  repoKeyParts,
  untrackedDirBasePath,
} from './repoGit'

const log = createLogger('repoGit.store')

export type { DiffStats, GitFilterTab, RepoGitRefreshOpts } from './repoGit'
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
  const workerKeys = new Map<string, Set<RepoKey>>()

  let gen = 0

  const get = (key: RepoKey): RepoGitState | undefined => repos[key]

  const indexKey = (key: RepoKey, workerId: string) => {
    if (!workerId)
      return
    let set = workerKeys.get(workerId)
    if (!set) {
      set = new Set()
      workerKeys.set(workerId, set)
    }
    set.add(key)
  }

  const unindexKey = (key: RepoKey, workerId?: string) => {
    const id = workerId || repoKeyParts(key).workerId
    if (!id)
      return
    const set = workerKeys.get(id)
    if (!set)
      return
    set.delete(key)
    if (set.size === 0)
      workerKeys.delete(id)
  }

  const keysForWorker = (workerId: string): readonly RepoKey[] =>
    [...(workerKeys.get(workerId) ?? [])]

  const upsert = (key: RepoKey, patch: Partial<RepoGitState>) => {
    const prev = repos[key]
    const { files, ...rest } = patch
    setRepos(produce((map) => {
      const base = map[key] ?? emptyRepoState()
      map[key] = { ...base, ...rest }
    }))
    const workerId = patch.workerId || prev?.workerId || repoKeyParts(key).workerId
    if (prev?.workerId && prev.workerId !== workerId)
      unindexKey(key, prev.workerId)
    indexKey(key, workerId)
    if (files)
      setRepos(key, 'files', reconcile(files, { key: 'path' }))
  }

  const clear = (key: RepoKey) => {
    const prev = repos[key]
    setRepos(produce((map) => {
      delete map[key]
    }))
    if (prev)
      unindexKey(key, prev.workerId)
    else
      unindexKey(key)
  }

  const clearAll = () => {
    setRepos({})
    workerKeys.clear()
  }

  const realignFocusedKeyAfterRefresh = (
    writtenKey: RepoKey | undefined,
    workerId: string,
    path: string,
    hintKey?: RepoKey,
  ) => {
    if (!writtenKey)
      return
    const focused = focusedKey()
    const probeKey = repoKey(workerId, path)
    if (focused === hintKey || focused === probeKey || focused === writtenKey) {
      if (focused !== writtenKey)
        setFocusedKeySignal(writtenKey)
    }
    if (hintKey && hintKey !== writtenKey) {
      const orphan = repos[hintKey]
      if (orphan && !orphan.toplevel)
        clear(hintKey)
    }
  }

  const refresh = async (workerId: string, path: string, opts?: RepoGitRefreshOpts): Promise<RepoKey | undefined> => {
    if (!workerId || !path)
      return undefined
    const nonRepoKey = opts?.repoKey ?? (workerId && path ? repoKey(workerId, path) : undefined)
    gen += 1
    const mine = gen
    setLoading(true)
    let writtenKey: RepoKey | undefined
    try {
      const resp = await workerRpc.getGitFileStatus(workerId, { workerId, path })
      if (mine !== gen)
        return undefined
      const mapped = patchFromGetGitFileStatus(workerId, resp)
      if (!mapped) {
        const lookup = { get, repos: () => repos as Readonly<Record<RepoKey, RepoGitState>>, keysForWorker }
        if (nonRepoKey && !hasHealthyRepoForProbe(lookup, workerId, path, nonRepoKey)) {
          const nonRepo = patchFromNonRepoGetGitFileStatus(workerId, resp, nonRepoKey)
          upsert(nonRepo.key, nonRepo.patch)
          writtenKey = nonRepo.key
        }
        else if (hasHealthyRepoForProbe(lookup, workerId, path, nonRepoKey)) {
          log.warn('ignored non-repo git status response; keeping last-good repo state', { workerId, path })
          writtenKey = findCanonicalRepoKey(lookup, workerId, path) ?? nonRepoKey
        }
        realignFocusedKeyAfterRefresh(writtenKey, workerId, path, nonRepoKey)
        return writtenKey
      }
      writtenKey = applyFullGitStatusUpsert({ get, upsert }, mapped)
      realignFocusedKeyAfterRefresh(writtenKey, workerId, path, nonRepoKey)
      return writtenKey
    }
    catch (err) {
      if (mine !== gen)
        return undefined
      log.warn('failed to refresh git file status', err)
      // Unstick an optimistic branch pin so metadata broadcasts can proceed.
      const pinKeys = new Set<RepoKey>()
      if (nonRepoKey)
        pinKeys.add(nonRepoKey)
      const canonical = findCanonicalRepoKey(
        { get, repos: () => repos as Readonly<Record<RepoKey, RepoGitState>>, keysForWorker },
        workerId,
        path,
      )
      if (canonical)
        pinKeys.add(canonical)
      for (const key of pinKeys) {
        if (get(key)?.branchPinnedUntilRefresh)
          upsert(key, { branchPinnedUntilRefresh: false })
      }
      return undefined
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
    keysForWorker,
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
