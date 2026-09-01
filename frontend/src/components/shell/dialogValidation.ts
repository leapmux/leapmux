/** Shared validation logic for New Workspace / Agent / Terminal dialogs. */

import type { GitModeIntent } from '~/hooks/useGitModeState'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { GitMode, isChangeBranchMode } from '~/hooks/useGitModeState'

interface BaseDialogState {
  submitting: boolean
  workerId: string
  workingDir: string
  /**
   * Why a precondition OUTSIDE the dialog blocks creation — e.g. no
   * workspace to place the new tab in. A non-empty string disables submit
   * and is shown as the reason. Distinct from the field checks: the
   * submit it prevents would create the worker-side resource first and
   * orphan it when placement refuses. Carrying the reason (not a bare
   * boolean) keeps the gate and the notice from disagreeing about the
   * same moment.
   */
  blockedReason?: string
  /**
   * Validation error for the dialog's Title field, or null when it is
   * acceptable. Every tab-creating dialog carries the same field with the
   * same rule, so the check lives on the base rather than once per dialog:
   * a new dialog that forgets it fails to compile instead of shipping a
   * submit that sends an empty title.
   */
  titleError: string | null
  /**
   * The currently-active git-mode intent. Optional so dialogs without
   * git options can skip it entirely — adding a new git mode then only
   * touches `useGitModeState` and the switch in `isGitModeInvalid`.
   */
  git?: GitModeIntent
}

/**
 * The state of a dialog that opens an AGENT tab.
 *
 * New Workspace uses it too: its submit spawns the workspace's first agent, so
 * it renders the same provider picker and the same resume field, and it
 * restricts submit by exactly the same three things. The two had separate
 * interfaces and separate functions with identical bodies, which is one rule
 * written twice.
 */
interface AgentDialogState extends BaseDialogState {
  noProviders: boolean
  sessionIdError: string | null
}

interface TerminalDialogState extends BaseDialogState {
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
 * treated as valid — the dialog's own rule decides submitability.
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

// The submit rule shared by every worker-bound dialog: an in-flight
// submission, missing worker selection, blank working directory, an empty or
// over-long title, or an invalid git-mode payload always disables submit
// regardless of the dialog-specific checks layered on top.
function isBaseDialogInvalid(state: BaseDialogState): boolean {
  return state.submitting
    || !!state.blockedReason
    || !state.workerId
    || !state.workingDir.trim()
    || !!state.titleError
    || isGitModeInvalid(state.git)
}

export function isAgentCreateDisabled(state: AgentDialogState): boolean {
  return isBaseDialogInvalid(state)
    || state.noProviders
    || !!state.sessionIdError
}

export function isTerminalCreateDisabled(state: TerminalDialogState): boolean {
  return isBaseDialogInvalid(state)
    || !state.shell
}

/**
 * ChangeBranchDialog's submit state.
 *
 * It EXTENDS the base like every other dialog, so `submitting`,
 * `blockedReason`, `workerId`, `workingDir`, `git` and `titleError` are all
 * settled by one function. It used to re-implement the first two by hand,
 * which is a second copy of the shared rule that only stayed correct by
 * inspection.
 *
 * `git` is REQUIRED here, unlike on the base: this dialog exists to change a
 * branch, so it always has a mode.
 */
interface ChangeBranchDialogState extends BaseDialogState {
  git: GitModeIntent
  /**
   * When the active mode is `CreateWorktree`, the dialog asks the user
   * what kind of tab to open in the new worktree (AGENT or TERMINAL),
   * so the submit rule depends on the picked tab type plus its
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
 *
 * The CALLER decides whether a conditionally-rendered field applies, and
 * passes null when it does not — the title is rendered only in
 * `CreateWorktree`, so the other modes pass no title error and an emptied
 * title cannot block a plain branch switch. That is the same arrangement
 * `blockedReason` already used, so the two conditional fields now follow one
 * rule instead of two.
 */
export function isChangeBranchSubmitDisabled(state: ChangeBranchDialogState): boolean {
  if (isBaseDialogInvalid(state))
    return true
  if (!isChangeBranchMode(state.git.mode))
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
