/** Shared validation logic for New Workspace / Agent / Terminal dialogs. */

interface BaseDialogState {
  submitting: boolean
  workerId: string
  workingDir: string
  createWorktree: boolean
  worktreeBranchError: string | null
}

interface WorkspaceDialogState extends BaseDialogState {
  titleError: string | null
}

interface TerminalDialogState extends BaseDialogState {
  shell: string
}

export function isWorkspaceCreateDisabled(state: WorkspaceDialogState): boolean {
  return state.submitting
    || !state.workerId
    || !state.workingDir.trim()
    || !!state.titleError
    || (state.createWorktree && !!state.worktreeBranchError)
}

export function isAgentCreateDisabled(state: BaseDialogState): boolean {
  return state.submitting
    || !state.workerId
    || !state.workingDir.trim()
    || (state.createWorktree && !!state.worktreeBranchError)
}

export function isTerminalCreateDisabled(state: TerminalDialogState): boolean {
  return state.submitting
    || !state.workerId
    || !state.workingDir.trim()
    || !state.shell
    || (state.createWorktree && !!state.worktreeBranchError)
}
