/** Shared validation logic for New Workspace / Agent / Terminal dialogs. */

import type { GitMode } from '~/hooks/createWorkerDialogState'

interface BaseDialogState {
  submitting: boolean
  workerId: string
  workingDir: string
  gitMode: GitMode
  worktreeBranchError: string | null
  checkoutBranch: string
  createBranchError: string | null
  useWorktreePath: string
}

interface AgentDialogState extends BaseDialogState {
  noProviders: boolean
  sessionIdError: string | null
}

interface WorkspaceDialogState extends AgentDialogState {
  titleError: string | null
}

interface TerminalDialogState extends BaseDialogState {
  shell: string
}

function isGitModeInvalid(state: BaseDialogState): boolean {
  switch (state.gitMode) {
    case 'current':
      return false
    case 'switch-branch':
      return !state.checkoutBranch
    case 'create-branch':
      return !!state.createBranchError
    case 'create-worktree':
      return !!state.worktreeBranchError
    case 'use-worktree':
      return !state.useWorktreePath
    default:
      return false
  }
}

export function isWorkspaceCreateDisabled(state: WorkspaceDialogState): boolean {
  return state.submitting
    || !state.workerId
    || !state.workingDir.trim()
    || state.noProviders
    || !!state.titleError
    || !!state.sessionIdError
    || isGitModeInvalid(state)
}

export function isAgentCreateDisabled(state: AgentDialogState): boolean {
  return state.submitting
    || !state.workerId
    || !state.workingDir.trim()
    || state.noProviders
    || !!state.sessionIdError
    || isGitModeInvalid(state)
}

export function isTerminalCreateDisabled(state: TerminalDialogState): boolean {
  return state.submitting
    || !state.workerId
    || !state.workingDir.trim()
    || !state.shell
    || isGitModeInvalid(state)
}
