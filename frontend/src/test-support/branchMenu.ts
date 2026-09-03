import type { Mock } from 'vitest'
import type { BranchMenuActions, BranchRefActions } from '~/components/workspace/branchActions'
import { vi } from 'vitest'

/** A {@link BranchMenuActions} bundle whose every action is a spy. */
export type StubBranchMenuActions = { [K in keyof BranchMenuActions]: Mock<BranchMenuActions[K]> }

/** A {@link BranchRefActions} bundle whose every action is a spy. */
export type StubBranchRefActions = { [K in keyof BranchRefActions]: Mock<BranchRefActions[K]> }

/**
 * A complete BOUND bundle, for any surface that renders a branch context menu
 * against one branch: `BranchContextMenu` itself and the composer.
 *
 * Shared rather than rewritten per suite, so a seventh action added to
 * {@link BranchMenuActions} makes every caller fail to compile in one place
 * instead of silently leaving the new item unwired in five of them.
 */
export function stubBranchMenuActions(): StubBranchMenuActions {
  return {
    onChangeBranch: vi.fn(),
    onDeleteBranch: vi.fn(),
    onNewAgent: vi.fn(),
    onNewAgentAdvanced: vi.fn(),
    onNewTerminalWithShell: vi.fn(),
    onNewTerminalAdvanced: vi.fn(),
  }
}

/**
 * A complete UNBOUND bundle, for the sidebar: every spy takes the row's
 * `BranchRef` as its first argument, so a test can assert which row fired.
 */
export function stubBranchRefActions(): StubBranchRefActions {
  return {
    onChangeBranch: vi.fn(),
    onDeleteBranch: vi.fn(),
    onNewAgent: vi.fn(),
    onNewAgentAdvanced: vi.fn(),
    onNewTerminalWithShell: vi.fn(),
    onNewTerminalAdvanced: vi.fn(),
  }
}
