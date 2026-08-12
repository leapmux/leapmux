import type { GitBranchEntry, InspectBranchDeletionResponse, InspectWorktreeRemovalResponse } from '~/generated/leapmux/v1/git_pb'

/**
 * Fixtures for the git inspect responses the branch dialogs drive.
 *
 * Two suites drive the same RPC shapes: `DeleteBranchDialog.test.tsx` renders
 * the dialog directly, and `AppShellDialogs.test.tsx` renders it through the
 * real `<Show>` composition. They held byte-identical copies of these
 * builders, and the second copy dropped the worktree conventions that the
 * first one documents, so each suite had to re-encode them at the call site.
 * One home keeps the worker contract in one place.
 */

/**
 * The removal preflight's verdict. An empty reason is the normal answer, so it
 * is the default: a fixture that blocks by accident disables Delete for every
 * case that did not ask for it.
 */
export function makeWorktreeRemovalResp(blockedReason = ''): InspectWorktreeRemovalResponse {
  return { $typeName: 'leapmux.v1.InspectWorktreeRemovalResponse', blockedReason }
}

export function makeBranches(names: string[]): GitBranchEntry[] {
  return names.map(name => ({
    $typeName: 'leapmux.v1.GitBranchEntry',
    name,
    isRemote: false,
  } as GitBranchEntry))
}

export function makeInspectResp(overrides: Partial<InspectBranchDeletionResponse> & Partial<{
  diffAdded: number
  diffDeleted: number
  diffUntracked: number
  unpushedCommitCount: number
  hasUncommittedChanges: boolean
  upstreamExists: boolean
  remoteBranchMissing: boolean
  originExists: boolean
  canPush: boolean
  /** Convenience: pass branch names; converted to GitBranchEntry rows. */
  branchNames: string[]
}> = {}): InspectBranchDeletionResponse {
  const {
    diffAdded = 0,
    diffDeleted = 0,
    diffUntracked = 0,
    unpushedCommitCount = 0,
    hasUncommittedChanges = false,
    upstreamExists = true,
    remoteBranchMissing = false,
    originExists = true,
    canPush = false,
    gitState,
    branchNames,
    branches,
    ...rest
  } = overrides
  // Default non-worktree responses include a picker list (the doomed
  // branch is in there too — the dialog filters it out). The worktree
  // path leaves `branches` empty to mirror the worker's contract.
  const isWorktree = rest.isWorktree ?? false
  const defaultBranches: GitBranchEntry[] = isWorktree ? [] : makeBranches(['main', 'doomed'])
  return {
    $typeName: 'leapmux.v1.InspectBranchDeletionResponse',
    isWorktree,
    worktreePath: '',
    // Worktree responses thread the DB row id so the dialog can tell a
    // tracked worktree from an untracked one; non-worktree leaves it empty.
    worktreeId: isWorktree ? 'wt-1' : '',
    branchName: 'doomed',
    branches: branches ?? (branchNames ? makeBranches(branchNames) : defaultBranches),
    gitState: gitState ?? ({
      $typeName: 'leapmux.v1.BranchGitState',
      diffAdded,
      diffDeleted,
      diffUntracked,
      unpushedCommitCount,
      hasUncommittedChanges,
      upstreamExists,
      remoteBranchMissing,
      originExists,
      canPush,
    } as InspectBranchDeletionResponse['gitState']),
    ...rest,
  } as InspectBranchDeletionResponse
}
