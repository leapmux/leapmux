import type { createRepoGitStore } from './repoGit.store'
import type { Tab } from './tab.types'
import type { GitFileStatusEntry, GitRepoStatus } from '~/generated/leapmux/v1/common_pb'
import type { GetGitFileStatusResponse } from '~/generated/leapmux/v1/git_pb'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'

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

/** Options for {@link RepoGitStore.refresh}. */
export interface RepoGitRefreshOpts {
  /**
   * Repo identity to clear when the probe fails or reports a non-repo path.
   * Omitted when the tab has not resolved `gitToplevel` yet.
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

/** Map a worker git-status proto onto store fields. */
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

/** Join a tab's repo identity to the keyed store for UI reads. */
export function repoGitView(
  tab: { workerId?: string, gitToplevel?: string },
  store: RepoGitStore,
): RepoGitView {
  const key = repoKeyFromTab(tab)
  if (!key)
    return EMPTY_VIEW
  const state = store.get(key)
  if (!state)
    return { ...EMPTY_VIEW, key, toplevel: tab.gitToplevel }
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
  return repoGitView(tab, store)
}
