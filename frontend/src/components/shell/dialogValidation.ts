/** Shared validation logic for New Workspace / Agent / Terminal dialogs. */

import type { GitModeIntent } from '~/hooks/useGitModeState'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { GitMode, isChangeBranchMode } from '~/hooks/useGitModeState'

interface BaseDialogState {
  submitting: boolean
  workerId: string
  workingDir: string
  /**
   * The currently-active git-mode intent. Optional so dialogs without
   * git options can skip it entirely — adding a new git mode then only
   * touches `useGitModeState` and the switch in `isGitModeInvalid`.
   */
  git?: GitModeIntent
}

interface ScopedDialogState extends BaseDialogState {
  workspaceId: string
}

interface AgentDialogState extends ScopedDialogState {
  noProviders: boolean
  sessionIdError: string | null
}

interface WorkspaceDialogState extends BaseDialogState {
  noProviders: boolean
  sessionIdError: string | null
  titleError: string | null
}

interface TerminalDialogState extends ScopedDialogState {
  shell: string
}

/**
 * Returns true when the active git mode is missing required fields. The
 * CreateBranch and CreateWorktree branches require a non-empty branch
 * name; the base branch is OPTIONAL because the worker's createBranchInDir
 * runs `git checkout -b <name>` against HEAD when no base is supplied.
 * That's the only sensible default in two cases the dialog can't seed
 * from `currentBranch`:
 *
 *   - Detached HEAD: `currentBranch` is empty, so the picker stays
 *     blank. A required-base gate locks the user out of creating a
 *     branch even though the server would have happily created one
 *     from HEAD (i.e. from the SHA they're sitting on).
 *   - Unborn HEAD (fresh `git init` with no commits yet): same shape.
 *
 * When `intent` is undefined (dialog has no git options), every mode is
 * treated as valid — the dialog's own gating decides submitability.
 */
export function isGitModeInvalid(intent: GitModeIntent | undefined): boolean {
  if (!intent)
    return false
  switch (intent.mode) {
    case GitMode.Current:
      return false
    case GitMode.SwitchBranch:
      return !intent.checkoutBranch
    case GitMode.CreateBranch:
      return !intent.createBranch || !!intent.createBranchError
    case GitMode.CreateWorktree:
      return !intent.worktreeBranch || !!intent.worktreeBranchError
    case GitMode.UseWorktree:
      return !intent.useWorktreePath
  }
}

// Submit-gating shared by every worker-bound dialog: an in-flight
// submission, missing worker selection, blank working directory, or an
// invalid git-mode payload always disables submit regardless of the
// dialog-specific checks layered on top.
function isBaseDialogInvalid(state: BaseDialogState): boolean {
  return state.submitting
    || !state.workerId
    || !state.workingDir.trim()
    || isGitModeInvalid(state.git)
}

export function isWorkspaceCreateDisabled(state: WorkspaceDialogState): boolean {
  return isBaseDialogInvalid(state)
    || state.noProviders
    || !!state.titleError
    || !!state.sessionIdError
}

export function isAgentCreateDisabled(state: AgentDialogState): boolean {
  return isBaseDialogInvalid(state)
    || !state.workspaceId
    || state.noProviders
    || !!state.sessionIdError
}

export function isTerminalCreateDisabled(state: TerminalDialogState): boolean {
  return isBaseDialogInvalid(state)
    || !state.workspaceId
    || !state.shell
}

interface ChangeBranchDialogState {
  submitting: boolean
  git: GitModeIntent
  /**
   * When the active mode is `CreateWorktree`, the dialog asks the user
   * what kind of tab to open in the new worktree (AGENT or TERMINAL),
   * so submit gating depends on the picked tab type plus its
   * tab-type-specific requirements.
   */
  worktreeTabType: TabType.AGENT | TabType.TERMINAL
  noProviders: boolean
  shell: string
}

/**
 * Submit gate for ChangeBranchDialog. The dialog renders only
 * `SwitchBranch` / `CreateBranch` / `CreateWorktree`, so any other mode
 * is treated as invalid defensively in case `state.gitMode()` hasn't
 * yet caught up with the initial intent seeded via
 * `useGitModeState(initial)`.
 *
 * SwitchBranch carries its own `checkoutBranchError` (mirroring
 * CreateBranch / CreateWorktree) so a destination that resolves to the
 * current branch — picked directly or via a remote ref that strips to
 * current — disables submit. Other dialogs (NewAgent / NewTerminal /
 * NewWorkspace) deliberately ignore this field: there, SwitchBranch is
 * a prep step before opening the new tab, so a no-op switch is still a
 * valid prefix to a real operation.
 */
export function isChangeBranchSubmitDisabled(state: ChangeBranchDialogState): boolean {
  if (state.submitting)
    return true
  if (!isChangeBranchMode(state.git.mode))
    return true
  if (isGitModeInvalid(state.git))
    return true
  if (state.git.mode === GitMode.SwitchBranch && !!state.git.checkoutBranchError)
    return true
  if (state.git.mode === GitMode.CreateWorktree) {
    if (state.worktreeTabType === TabType.AGENT)
      return state.noProviders
    return !state.shell
  }
  return false
}
