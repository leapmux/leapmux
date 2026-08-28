import type { GitFilterTab, RepoGitRefreshOpts, RepoGitState, RepoKey } from './repoGit'
import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import { createMemo, createSignal, untrack } from 'solid-js'
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
  markNonRepoProbeIgnored,
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

/**
 * How many repo entries one page session keeps.
 *
 * Nothing else reclaims them: a dropped link keeps its entries, and so does a
 * deregistration, because the tab rows they label survive both. The cap is the
 * only limit, and each entry holds an uncapped `files` list.
 *
 * Set high on purpose. A real session touches a handful of repositories, so
 * eviction should reach only a session that visited hundreds of distinct
 * directories. See `evictLeastRecentlyUsed` for what it refuses to evict.
 */
const MAX_REPO_ENTRIES = 256

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
  const [workerKeyEpoch, setWorkerKeyEpoch] = createSignal(0)
  const workerKeys = new Map<string, Set<RepoKey>>()

  /**
   * Least-recently-used order, newest last. A plain `Map` keeps insertion
   * order, so `delete` then `set` moves a key to the end.
   *
   * NOT reactive on purpose. `get` touches this on a read, and a signal write
   * inside a tracked read would loop.
   */
  const touchOrder = new Map<RepoKey, true>()

  /** Global clock for ordering; each probe path has its own generation slot. */
  let clock = 0
  const probeGen = new Map<string, number>()
  const inflightByProbe = new Map<string, { gen: number, keys: Set<RepoKey> }>()
  const lastCompletedByKey = new Map<RepoKey, { gen: number, keptPin: boolean }>()
  let loadingCount = 0

  /**
   * Read WITHOUT touching the LRU order.
   *
   * The internal key scans (`findCanonicalRepoKey`, the pin helpers) read every
   * key a worker owns. Touching there would make every entry equally recent and
   * leave the order meaningless, so they use this.
   */
  const peek = (key: RepoKey): RepoGitState | undefined => repos[key]

  /**
   * Read AND mark the entry as recently used.
   *
   * This is the tab-facing read: `repoGitView` calls it for each tab the
   * sidebar renders, so an entry that backs a live tab is touched on every
   * render and can never be the least recently used one. That is what keeps
   * eviction away from the rows whose branch label this store exists to
   * supply.
   */
  const get = (key: RepoKey): RepoGitState | undefined => {
    const state = repos[key]
    if (state) {
      touchOrder.delete(key)
      touchOrder.set(key, true)
    }
    return state
  }

  const bumpWorkerKeyIndex = () => setWorkerKeyEpoch(n => n + 1)

  const beginLoading = () => {
    loadingCount += 1
    setLoading(true)
  }

  const endLoading = () => {
    loadingCount = Math.max(0, loadingCount - 1)
    if (loadingCount === 0)
      setLoading(false)
  }

  const probeIdFor = (workerId: string, path: string) => `${workerId}\0${path}`

  const indexKey = (key: RepoKey, workerId: string) => {
    if (!workerId)
      return
    let set = workerKeys.get(workerId)
    if (!set) {
      set = new Set()
      workerKeys.set(workerId, set)
    }
    if (set.has(key))
      return
    set.add(key)
    bumpWorkerKeyIndex()
  }

  const unindexKey = (key: RepoKey, workerId?: string) => {
    const id = workerId || repoKeyParts(key).workerId
    if (!id)
      return
    const set = workerKeys.get(id)
    if (!set)
      return
    if (!set.delete(key))
      return
    if (set.size === 0)
      workerKeys.delete(id)
    bumpWorkerKeyIndex()
  }

  const keysForWorker = (workerId: string): readonly RepoKey[] => {
    // Track epoch so Solid recomputes when the index changes.
    workerKeyEpoch()
    return [...(workerKeys.get(workerId) ?? [])]
  }

  const pruneCompletedIfIdle = (key: RepoKey) => {
    const completed = lastCompletedByKey.get(key)
    if (!completed || completed.keptPin || peek(key)?.branchPinnedUntilRefresh)
      return
    for (const inflight of inflightByProbe.values()) {
      if (inflight.gen < completed.gen)
        return
    }
    lastCompletedByKey.delete(key)
  }

  const dropCompletedKeepPinWhenUnpinned = (key: RepoKey) => {
    const completed = lastCompletedByKey.get(key)
    if (completed && !peek(key)?.branchPinnedUntilRefresh)
      completed.keptPin = false
    pruneCompletedIfIdle(key)
  }

  const resetCompletedKeepPinForNewStamp = (key: RepoKey) => {
    const completed = lastCompletedByKey.get(key)
    if (completed)
      completed.keptPin = false
  }

  const clear = (key: RepoKey) => {
    const prev = untrack(() => repos[key])
    setRepos(produce((map) => {
      delete map[key]
    }))
    lastCompletedByKey.delete(key)
    touchOrder.delete(key)
    if (prev)
      unindexKey(key, prev.workerId)
    else
      unindexKey(key)
  }

  /**
   * Evict the least recently used entries down to {@link MAX_REPO_ENTRIES}.
   *
   * Three keys are never evicted, because dropping them would recreate the
   * defect this store exists to avoid -- a tab row that keeps `gitToplevel`
   * while its entry is gone renders under a repository with no branch name:
   *
   *  - the key just written, which the caller is about to read;
   *  - the focused key, which the Files section renders right now;
   *  - a key with a branch pin, which holds in-flight optimistic state.
   *
   * A key that backs a live tab is touched by `get` on every sidebar render,
   * so it stays at the recent end and eviction never reaches it. Only a
   * directory the session visited once and abandoned goes cold.
   */
  const evictLeastRecentlyUsed = (justWritten: RepoKey) => {
    if (touchOrder.size <= MAX_REPO_ENTRIES)
      return
    const focused = untrack(focusedKey)
    for (const key of [...touchOrder.keys()]) {
      if (touchOrder.size <= MAX_REPO_ENTRIES)
        return
      if (key === justWritten || key === focused)
        continue
      if (untrack(() => repos[key])?.branchPinnedUntilRefresh)
        continue
      clear(key)
    }
  }

  const upsert = (key: RepoKey, patch: Partial<RepoGitState>) => {
    // Write APIs must not track. A seed/upsert from JSX or an effect that
    // also reads this key would otherwise loop: read prev → write → re-run.
    const prev = untrack(() => repos[key])
    const { files, ...rest } = patch
    setRepos(produce((map) => {
      const base = map[key] ?? emptyRepoState()
      map[key] = { ...base, ...rest }
    }))
    touchOrder.delete(key)
    touchOrder.set(key, true)
    const workerId = patch.workerId || prev?.workerId || repoKeyParts(key).workerId
    if (prev?.workerId && prev.workerId !== workerId)
      unindexKey(key, prev.workerId)
    indexKey(key, workerId)
    if (files)
      setRepos(key, 'files', reconcile(files, { key: 'path' }))
    if ('branchPinnedUntilRefresh' in rest && rest.branchPinnedUntilRefresh === false)
      dropCompletedKeepPinWhenUnpinned(key)
    if ('branchPinnedUntilRefresh' in rest && rest.branchPinnedUntilRefresh === true)
      resetCompletedKeepPinForNewStamp(key)
    evictLeastRecentlyUsed(key)
  }

  /** Keys this refresh may have stamped a branch pin onto. */
  const pinKeysForProbe = (workerId: string, path: string, hintKey?: RepoKey): Set<RepoKey> => {
    const pinKeys = new Set<RepoKey>()
    if (hintKey)
      pinKeys.add(hintKey)
    pinKeys.add(repoKey(workerId, path))
    const canonical = findCanonicalRepoKey(
      { get: peek, repos: () => repos as Readonly<Record<RepoKey, RepoGitState>>, keysForWorker },
      workerId,
      path,
    )
    if (canonical)
      pinKeys.add(canonical)
    return pinKeys
  }

  const clearBranchPins = (keys: Iterable<RepoKey>, opts?: { respectCompletedKeep?: boolean }) => {
    for (const key of keys) {
      const completed = lastCompletedByKey.get(key)
      if (opts?.respectCompletedKeep && completed?.keptPin && peek(key)?.branchPinnedUntilRefresh)
        continue
      if (peek(key)?.branchPinnedUntilRefresh)
        upsert(key, { branchPinnedUntilRefresh: false })
    }
  }

  /**
   * Release every optimistic branch pin this worker holds, keeping the branch
   * values themselves.
   *
   * A pin means "a branch change succeeded here, so ignore a metadata broadcast
   * that still reports the old branch". A dropped link ends that claim: the
   * checkout may never have completed, and the pin outlives it. Only a refresh
   * that AGREES clears a pin, and a background tab issues no refresh -- so
   * without this the sidebar can show a stamped branch for the rest of the page
   * while the checkout sits on another one.
   *
   * The branch value stays, because it is still the last thing anyone knew.
   */
  const releaseBranchPinsForWorker = (workerId: string) => {
    if (!workerId)
      return
    clearBranchPins(workerKeys.get(workerId) ?? [])
  }

  const clearAll = () => {
    const hadKeys = workerKeys.size > 0
    setRepos({})
    workerKeys.clear()
    touchOrder.clear()
    lastCompletedByKey.clear()
    probeGen.clear()
    inflightByProbe.clear()
    if (hadKeys)
      bumpWorkerKeyIndex()
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

  const laterRefreshCoversKey = (mine: number, key: RepoKey): boolean => {
    for (const inflight of inflightByProbe.values()) {
      if (inflight.gen > mine && inflight.keys.has(key))
        return true
    }
    const completedGen = lastCompletedByKey.get(key)
    return completedGen !== undefined && completedGen.gen > mine
  }

  const releaseStaleRefreshPins = (mine: number, myKeys: Set<RepoKey>) => {
    for (const key of myKeys) {
      if (laterRefreshCoversKey(mine, key))
        continue
      // An earlier completed refresh already applied pin policy for this key.
      // A cancelled nested/canonical probe must not undo that keep.
      const completed = lastCompletedByKey.get(key)
      if (completed?.keptPin && get(key)?.branchPinnedUntilRefresh)
        continue
      if (get(key)?.branchPinnedUntilRefresh)
        upsert(key, { branchPinnedUntilRefresh: false })
    }
  }

  // There is deliberately NO `clearForWorker`. Both callers were removed,
  // because dropping a worker's entries is never right while its tab rows
  // survive: a row keeps `gitToplevel`, the sidebar groups on that field, and
  // the branch label comes from here -- so a cleared entry puts every tab of
  // that worker under its repo with no branch name. Neither a dropped link nor
  // a deregistration removes the rows, so neither may remove the entries.
  //
  // `clear(key)` still exists for the one case that IS right: a single key whose
  // repo identity was re-resolved elsewhere.

  /**
   * Refresh git file status for one probe path.
   *
   * Ordering and pin policy use four side maps:
   *   - `clock` / `probeGen` — per-probe generation; stale RPC results are dropped
   *   - `inflightByProbe` — keys a refresh may pin; blocks nested probes from clobbering
   *   - `lastCompletedByKey` — `{ gen, keptPin }` for apply ordering vs pin retention
   *   - `workerKeys` — Solid-indexed repo keys per worker (for canonical lookup)
   */
  const refresh = async (workerId: string, path: string, opts?: RepoGitRefreshOpts): Promise<RepoKey | undefined> => {
    if (!workerId || !path)
      return undefined
    const nonRepoKey = opts?.repoKey ?? (workerId && path ? repoKey(workerId, path) : undefined)
    clock += 1
    const mine = clock
    const probeId = probeIdFor(workerId, path)
    probeGen.set(probeId, mine)
    const myKeys = pinKeysForProbe(workerId, path, nonRepoKey)
    inflightByProbe.set(probeId, { gen: mine, keys: myKeys })

    const canApplyToKey = (targetKey: RepoKey | undefined): targetKey is RepoKey => {
      if (!targetKey)
        return false
      if (laterRefreshCoversKey(mine, targetKey)) {
        releaseStaleRefreshPins(mine, myKeys)
        return false
      }
      return true
    }

    const recordCompletedApply = (appliedKey: RepoKey) => {
      lastCompletedByKey.set(appliedKey, {
        gen: mine,
        keptPin: Boolean(get(appliedKey)?.branchPinnedUntilRefresh),
      })
    }

    beginLoading()
    let writtenKey: RepoKey | undefined
    try {
      const resp = await workerRpc.getGitFileStatus(workerId, { workerId, path })
      if (probeGen.get(probeId) !== mine) {
        releaseStaleRefreshPins(mine, myKeys)
        return undefined
      }
      const mapped = patchFromGetGitFileStatus(workerId, resp)
      if (!mapped) {
        const lookup = { get: peek, repos: () => repos as Readonly<Record<RepoKey, RepoGitState>>, keysForWorker }
        if (nonRepoKey && !hasHealthyRepoForProbe(lookup, workerId, path, nonRepoKey)) {
          const nonRepo = patchFromNonRepoGetGitFileStatus(workerId, resp, nonRepoKey)
          if (canApplyToKey(nonRepo.key)) {
            upsert(nonRepo.key, nonRepo.patch)
            writtenKey = nonRepo.key
            recordCompletedApply(nonRepo.key)
          }
          else {
            const canonical = findCanonicalRepoKey(lookup, workerId, path)
            if (canonical)
              realignFocusedKeyAfterRefresh(canonical, workerId, path, nonRepoKey)
          }
        }
        else if (hasHealthyRepoForProbe(lookup, workerId, path, nonRepoKey)) {
          log.warn('ignored non-repo git status response; keeping last-good repo state', { workerId, path })
          writtenKey = findCanonicalRepoKey(lookup, workerId, path) ?? nonRepoKey
          // One answer can be transient, so this branch keeps the last-good
          // repo state. Mark the allowance as used. The next non-repo answer
          // for this path then writes the stub, because two answers in a row
          // are the worker's real report.
          //
          // The mark is what limits the allowance. A worker that goes offline
          // keeps its entries -- the branch is last-known state, and nothing
          // re-seeds a background tab after a drop. Before the mark existed,
          // an entry that outlived a deleted repo suppressed every later probe
          // for the life of the page.
          markNonRepoProbeIgnored({ ...lookup, upsert }, workerId, path, nonRepoKey)
        }
        realignFocusedKeyAfterRefresh(writtenKey, workerId, path, nonRepoKey)
        return writtenKey
      }
      if (!canApplyToKey(mapped.key)) {
        realignFocusedKeyAfterRefresh(mapped.key, workerId, path, nonRepoKey)
        return undefined
      }
      writtenKey = applyFullGitStatusUpsert({ get, upsert }, mapped)
      recordCompletedApply(writtenKey)
      realignFocusedKeyAfterRefresh(writtenKey, workerId, path, nonRepoKey)
      return writtenKey
    }
    catch (err) {
      if (probeGen.get(probeId) !== mine) {
        releaseStaleRefreshPins(mine, myKeys)
        return undefined
      }
      log.warn('failed to refresh git file status', err)
      // Unstick an optimistic branch pin so metadata broadcasts can proceed,
      // but do not undo a keep that an earlier completed refresh already set.
      clearBranchPins(myKeys, { respectCompletedKeep: true })
      return undefined
    }
    finally {
      endLoading()
      const tracked = inflightByProbe.get(probeId)
      if (tracked?.gen === mine)
        inflightByProbe.delete(probeId)
      if (probeGen.get(probeId) === mine)
        probeGen.delete(probeId)
      for (const key of myKeys)
        pruneCompletedIfIdle(key)
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
    releaseBranchPinsForWorker,
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
