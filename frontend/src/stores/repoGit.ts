import type { createRepoGitStore } from './repoGit.store'
import type { Tab } from './tab.types'
import type { GitFileStatusEntry, GitRepoStatus } from '~/generated/leapmux/v1/common_pb'
import type { GetGitFileStatusResponse } from '~/generated/leapmux/v1/git_pb'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { detectFlavor, relativeUnder } from '~/lib/paths'

export type RepoKey = `${string}\0${string}`

export interface DiffStats { added: number, deleted: number, untracked: number }

const ZERO_DIFF_STATS: DiffStats = { added: 0, deleted: 0, untracked: 0 }

export interface RepoGitState {
  workerId: string
  repoRoot: string
  toplevel: string
  branch: string
  originUrl: string
  isWorktree: boolean
  ahead: number
  behind: number
  conflicted: boolean
  stashed: boolean
  deleted: boolean
  renamed: boolean
  modified: boolean
  typeChanged: boolean
  added: boolean
  untracked: boolean
  diffAdded: number
  diffDeleted: number
  diffUntracked: number
  files: GitFileStatusEntry[]
  errorHint: string
  /**
   * When true, metadata broadcasts must not override `branch` until
   * `refresh()` or a full GetGitFileStatus upsert clears the pin.
   */
  branchPinnedUntilRefresh?: boolean
  /**
   * Set when a git-status proto or GetGitFileStatus response wrote this
   * entry. Distinguishes a real status seed from an optimistic branch stamp.
   */
  gitStatusSeen?: boolean
}

export interface RepoGitView {
  key: RepoKey | undefined
  branchLabel: string | undefined
  diffStats: DiffStats
  ahead: number
  behind: number
  conflicted: boolean
  stashed: boolean
  deleted: boolean
  renamed: boolean
  modified: boolean
  typeChanged: boolean
  added: boolean
  untracked: boolean
  isWorktree: boolean | undefined
  originUrl: string | undefined
  toplevel: string | undefined
  files: GitFileStatusEntry[] | undefined
  errorHint: string | undefined
  isGitRepo: boolean
}

export type GitFilterTab = 'all' | 'changed' | 'staged' | 'unstaged'

export type RepoGitStore = ReturnType<typeof createRepoGitStore>

/** Minimal store surface for repo-key resolution helpers. */
export type RepoGitLookup = Pick<RepoGitStore, 'get' | 'repos'> & {
  /** Optional per-worker key index; avoids a full-map scan when present. */
  keysForWorker?: (workerId: string) => readonly RepoKey[]
}

/** Split a repo key into worker id and path. */
export function repoKeyParts(key: RepoKey): { workerId: string, path: string } {
  const i = key.indexOf('\0')
  return { workerId: key.slice(0, i), path: key.slice(i + 1) }
}

/** Options for {@link RepoGitStore.refresh}. */
export interface RepoGitRefreshOpts {
  /**
   * Repo identity for a non-repo response stub (`errorHint`, cleared git
   * fields). Falls back to `repoKey(workerId, path)` when omitted. RPC
   * failures keep the last-good entry and do not use this hint.
   */
  repoKey?: RepoKey
}

/** Path to pass to GetGitFileStatus: prefer repo toplevel over agent cwd. */
export function gitStatusProbePath(ctx: { gitToplevel?: string, workingDir?: string }): string {
  return ctx.gitToplevel || ctx.workingDir || ''
}

export function repoKey(workerId: string, gitToplevel: string): RepoKey {
  return `${workerId}\0${gitToplevel}`
}

export function repoKeyFromTab(tab: { workerId?: string, gitToplevel?: string }): RepoKey | undefined {
  const workerId = tab.workerId ?? ''
  const toplevel = tab.gitToplevel ?? ''
  if (!workerId || !toplevel)
    return undefined
  return repoKey(workerId, toplevel)
}

export function repoKeyFromStatus(workerId: string, status: GitRepoStatus | undefined): RepoKey | undefined {
  if (!status?.toplevel || !workerId)
    return undefined
  return repoKey(workerId, status.toplevel)
}

/**
 * Whether a git status entry names a whole untracked DIRECTORY rather than a
 * file. Git collapses an untracked directory into one `build/` entry, and the
 * trailing slash is the only marker.
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

/** Reduce a file list to the repo-wide totals. */
export function aggregateDiffStats(files: readonly GitFileStatusEntry[]): DiffStats {
  let added = 0
  let deleted = 0
  let untracked = 0
  for (const f of files) {
    if (f.unstagedStatus === GitFileStatusCode.UNTRACKED) {
      untracked++
    }
    else {
      added += f.linesAdded + f.stagedLinesAdded
      deleted += f.linesDeleted + f.stagedLinesDeleted
    }
  }
  return { added, deleted, untracked }
}

export function diffStatsFromRepo(
  state: Pick<RepoGitState, 'diffAdded' | 'diffDeleted' | 'diffUntracked'> | undefined,
): DiffStats {
  if (!state)
    return ZERO_DIFF_STATS
  return { added: state.diffAdded, deleted: state.diffDeleted, untracked: state.diffUntracked }
}

const EMPTY_VIEW: RepoGitView = {
  key: undefined,
  branchLabel: undefined,
  diffStats: ZERO_DIFF_STATS,
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
  isWorktree: undefined,
  originUrl: undefined,
  toplevel: undefined,
  files: undefined,
  errorHint: undefined,
  isGitRepo: false,
}

/** Map a worker git-status proto onto store fields (metadata only). */
export function protoToRepoGitPatch(
  workerId: string,
  status: GitRepoStatus | undefined,
): Partial<RepoGitState> | undefined {
  if (!status?.toplevel)
    return undefined
  return {
    workerId,
    toplevel: status.toplevel,
    branch: status.branch,
    originUrl: status.originUrl,
    isWorktree: status.isWorktree,
    ahead: status.ahead,
    behind: status.behind,
    conflicted: status.conflicted,
    stashed: status.stashed,
    deleted: status.deleted,
    renamed: status.renamed,
    modified: status.modified,
    typeChanged: status.typeChanged,
    added: status.added,
    untracked: status.untracked,
  }
}

export interface UpsertRepoGitFromProtoOpts {
  /** Delete this probe-path orphan key when repo identity resolves. */
  migrateErrorHintFrom?: RepoKey
}

/** Probe-path store key when a tab has not resolved `gitToplevel` yet. */
export function probePathOrphanKey(
  workerId: string,
  tab: { gitToplevel?: string, workingDir?: string },
): RepoKey | undefined {
  if (!workerId || tab.gitToplevel || !tab.workingDir)
    return undefined
  return repoKey(workerId, tab.workingDir)
}

export function migrateErrorHintFromForResolvedRepo(
  workerId: string,
  tab: { gitToplevel?: string, workingDir?: string },
  status: GitRepoStatus | undefined,
): RepoKey | undefined {
  const orphanKey = probePathOrphanKey(workerId, tab)
  const resolvedKey = repoKeyFromStatus(workerId, status)
  if (!orphanKey || !resolvedKey || orphanKey === resolvedKey)
    return undefined
  return orphanKey
}

/**
 * Canonical repo key for `probePath` when refresh already wrote toplevel-keyed
 * state but the tab still lacks `gitToplevel` (file tabs, probe-path orphans).
 */
export function findCanonicalRepoKey(
  store: RepoGitLookup,
  workerId: string,
  probePath: string,
): RepoKey | undefined {
  if (!workerId || !probePath)
    return undefined

  const exactKey = repoKey(workerId, probePath)
  const exact = store.get(exactKey)
  if (exact?.toplevel === probePath)
    return exactKey

  let best: { key: RepoKey, len: number } | undefined
  const keys = store.keysForWorker?.(workerId)
  const entries: Iterable<[string, RepoGitState | undefined]> = keys
    ? keys.map(k => [k, store.get(k)] as [string, RepoGitState | undefined])
    : Object.entries(store.repos())

  for (const [key, state] of entries) {
    if (!state || state.workerId !== workerId || !state.toplevel)
      continue
    if (state.toplevel === probePath)
      return key as RepoKey
    const flavor = detectFlavor(state.toplevel)
    if (relativeUnder(probePath, state.toplevel, flavor) !== null) {
      const len = state.toplevel.length
      if (!best || len > best.len)
        best = { key: key as RepoKey, len }
    }
  }
  return best?.key
}

/** True when the entry has fields beyond a branch stamp seed. */
export function hasHydratedRepoGitFields(state: RepoGitState): boolean {
  return Boolean(
    state.repoRoot
    || state.originUrl
    || state.files.length > 0
    || state.diffAdded
    || state.diffDeleted
    || state.diffUntracked
    || state.ahead
    || state.behind
    || state.modified
    || state.untracked
    || state.added
    || state.deleted
    || state.renamed
    || state.conflicted
    || state.stashed
    || state.isWorktree,
  )
}

/**
 * True when the entry is only an optimistic branch stamp
 * (`toplevel` + `branch` + pin) with no status payload yet.
 */
export function isStampOnlyRepoGitState(state: RepoGitState | undefined): boolean {
  if (!state?.toplevel || !state.branchPinnedUntilRefresh)
    return false
  return !state.gitStatusSeen && !hasHydratedRepoGitFields(state)
}

/**
 * True when the entry is a real repo identity worth keeping across a
 * transient non-repo response. Stamp-only seeds are excluded so a first
 * probe can still write an `errorHint` stub.
 */
export function hasPreservableRepoGitState(state: RepoGitState | undefined): boolean {
  if (!state?.toplevel)
    return false
  if (state.gitStatusSeen || hasHydratedRepoGitFields(state))
    return true
  return false
}

/** True when the store already holds a preservable repo for this probe path. */
export function hasHealthyRepoForProbe(
  store: RepoGitLookup,
  workerId: string,
  probePath: string,
  hintKey?: RepoKey,
): boolean {
  if (hintKey && hasPreservableRepoGitState(store.get(hintKey)))
    return true
  const canonical = findCanonicalRepoKey(store, workerId, probePath)
  if (!canonical)
    return false
  return hasPreservableRepoGitState(store.get(canonical))
}

/**
 * Apply a GetGitFileStatus upsert. Keeps a stamped branch until the RPC
 * reports the same branch name.
 */
export function applyFullGitStatusUpsert(
  store: Pick<RepoGitStore, 'get' | 'upsert'>,
  mapped: { key: RepoKey, patch: Partial<RepoGitState> },
): RepoKey {
  const prev = store.get(mapped.key)
  let branch = mapped.patch.branch ?? ''
  let branchPinnedUntilRefresh = false
  if (prev?.branchPinnedUntilRefresh) {
    const rpcBranch = mapped.patch.branch ?? ''
    if (rpcBranch === prev.branch) {
      branch = rpcBranch
    }
    else {
      branch = prev.branch
      branchPinnedUntilRefresh = true
    }
  }
  store.upsert(mapped.key, {
    ...mapped.patch,
    branch,
    branchPinnedUntilRefresh,
    gitStatusSeen: true,
  })
  return mapped.key
}

/**
 * Apply a git-status proto to the keyed store. Clears file-derived fields when
 * `toplevel` changes. Branch-only metadata updates keep the last file list
 * until a GetGitFileStatus refresh replaces it. Metadata broadcasts do not
 * carry diagnostics. Identity-stable upserts clear orphan-migration tips, but
 * keep a refresh-sourced hint on a hydrated entry.
 */
export function upsertRepoGitFromProtoStatus(
  store: RepoGitStore,
  workerId: string,
  status: GitRepoStatus | undefined,
  opts?: UpsertRepoGitFromProtoOpts,
): void {
  const patch = protoToRepoGitPatch(workerId, status)
  const key = repoKeyFromStatus(workerId, status)
  if (!patch || !key)
    return

  const prev = store.get(key)
  let next: Partial<RepoGitState> = { ...patch, gitStatusSeen: true }

  if (prev?.branchPinnedUntilRefresh)
    next.branch = prev.branch

  if (opts?.migrateErrorHintFrom) {
    // Repo identity resolved from a probe-path orphan. Drop the orphan always.
    // Do not copy its tip onto a healthy status entry — "not a git repository"
    // is stale once toplevel is known.
    store.clear(opts.migrateErrorHintFrom)
  }

  const toplevelChanged = !prev || next.toplevel !== prev.toplevel
  const branchChanged = Boolean(prev && next.branch !== prev.branch)

  if (toplevelChanged) {
    next = {
      ...next,
      diffAdded: 0,
      diffDeleted: 0,
      diffUntracked: 0,
      files: [],
      errorHint: '',
    }
  }
  else if (branchChanged && !prev?.branchPinnedUntilRefresh) {
    // Metadata broadcasts do not carry file lists. Keep the last file list
    // until GetGitFileStatus refresh replaces it — clearing here flashes an
    // empty Changed filter between status and refresh.
    next = {
      ...next,
      errorHint: prev?.errorHint && hasHydratedRepoGitFields(prev) ? prev.errorHint : '',
    }
  }
  else if (prev) {
    // Metadata does not carry diagnostics. Keep a refresh-sourced hint on a
    // hydrated entry; clear everything else (including leftover migrate tips).
    next.errorHint = hasHydratedRepoGitFields(prev) ? (prev.errorHint || '') : ''
  }

  store.upsert(key, next)
}

/** Repo key for reads; pass `ctx` and `store` when the probe path differs from tab fields. */
export function focusedRepoKeyFromTab(
  tab: { workerId?: string, gitToplevel?: string, workingDir?: string },
  ctx?: { gitToplevel?: string, workingDir?: string },
  store?: RepoGitLookup,
): RepoKey | undefined {
  const fromTab = repoKeyFromTab(tab)
  if (fromTab)
    return fromTab
  const workerId = tab.workerId ?? ''
  const path = gitStatusProbePath(ctx ?? tab)
  if (!workerId || !path)
    return undefined
  if (store) {
    const canonical = findCanonicalRepoKey(store, workerId, path)
    if (canonical)
      return canonical
    const probeKey = repoKey(workerId, path)
    const probeState = store.get(probeKey)
    if (probeState?.toplevel)
      return repoKey(workerId, probeState.toplevel)
  }
  return repoKey(workerId, path)
}

/** Map a GetGitFileStatus RPC response onto a repo upsert patch. */
export function patchFromGetGitFileStatus(
  workerId: string,
  resp: GetGitFileStatusResponse,
): { key: RepoKey, patch: Partial<RepoGitState> } | undefined {
  const status = resp.status
  const toplevel = status?.toplevel ?? ''
  if (!toplevel)
    return undefined
  const diffStats = aggregateDiffStats(resp.files)
  return {
    key: repoKey(workerId, toplevel),
    patch: {
      workerId,
      repoRoot: resp.repoRoot,
      toplevel,
      branch: status?.branch ?? '',
      originUrl: status?.originUrl ?? '',
      isWorktree: status?.isWorktree ?? false,
      ahead: status?.ahead ?? 0,
      behind: status?.behind ?? 0,
      conflicted: status?.conflicted ?? false,
      stashed: status?.stashed ?? false,
      deleted: status?.deleted ?? false,
      renamed: status?.renamed ?? false,
      modified: status?.modified ?? false,
      typeChanged: status?.typeChanged ?? false,
      added: status?.added ?? false,
      untracked: status?.untracked ?? false,
      diffAdded: diffStats.added,
      diffDeleted: diffStats.deleted,
      diffUntracked: diffStats.untracked,
      files: resp.files,
      errorHint: resp.errorHint,
    },
  }
}

/**
 * Map a successful but non-repo GetGitFileStatus response onto the hinted key.
 * Keeps `errorHint` for UI diagnostics and clears git fields so `isGitRepo` is false.
 */
export function patchFromNonRepoGetGitFileStatus(
  workerId: string,
  resp: GetGitFileStatusResponse,
  key: RepoKey,
): { key: RepoKey, patch: Partial<RepoGitState> } {
  return {
    key,
    patch: {
      workerId,
      repoRoot: resp.repoRoot,
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
      errorHint: resp.errorHint ?? '',
      gitStatusSeen: true,
      // A branch stamp may have set the pin before this probe returned
      // non-repo. Clear it so a later status cannot keep an empty branch.
      branchPinnedUntilRefresh: false,
    },
  }
}

/** Join a tab's repo identity to the keyed store for UI reads. */
export function repoGitView(
  tab: { workerId?: string, gitToplevel?: string, workingDir?: string },
  store: RepoGitStore,
  ctx?: { gitToplevel?: string, workingDir?: string },
): RepoGitView {
  const key = focusedRepoKeyFromTab(tab, ctx ?? tab, store)
  if (!key)
    return EMPTY_VIEW
  const state = store.get(key)
  if (!state) {
    // Prefer tab.gitToplevel. Never treat a probe-path store key as the repo root.
    return { ...EMPTY_VIEW, key, toplevel: tab.gitToplevel }
  }
  return {
    key,
    branchLabel: state.branch || undefined,
    diffStats: diffStatsFromRepo(state),
    ahead: state.ahead,
    behind: state.behind,
    conflicted: state.conflicted,
    stashed: state.stashed,
    deleted: state.deleted,
    renamed: state.renamed,
    modified: state.modified,
    typeChanged: state.typeChanged,
    added: state.added,
    untracked: state.untracked,
    isWorktree: state.isWorktree,
    originUrl: state.originUrl || undefined,
    toplevel: state.toplevel || undefined,
    files: state.files,
    errorHint: state.errorHint || undefined,
    isGitRepo: Boolean(state.toplevel),
  }
}

/** Tab-shaped git view for buildTree / tabBuildKey. */
export function repoGitViewForTab(tab: Tab, store: RepoGitStore): RepoGitView {
  return repoGitView(tab, store, tab)
}
