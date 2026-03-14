import { describe, expect, it } from 'vitest'
import {
  isAgentCreateDisabled,
  isTerminalCreateDisabled,
  isWorkspaceCreateDisabled,
} from '~/components/shell/dialogValidation'

const validBase = {
  submitting: false,
  workerId: 'worker-1',
  workingDir: '/home/user/project',
  gitMode: 'current' as const,
  worktreeBranchError: null,
  checkoutBranch: '',
  useWorktreePath: '',
}

describe('isWorkspaceCreateDisabled', () => {
  const valid = { ...validBase, titleError: null }

  it('returns false when all fields are valid', () => {
    expect(isWorkspaceCreateDisabled(valid)).toBe(false)
  })

  it('returns true when submitting', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, submitting: true })).toBe(true)
  })

  it('returns true when workerId is empty (no workers online)', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, workerId: '' })).toBe(true)
  })

  it('returns true when workingDir is empty', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, workingDir: '' })).toBe(true)
  })

  it('returns true when workingDir is whitespace-only', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, workingDir: '   ' })).toBe(true)
  })

  it('returns true when title has an error', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, titleError: 'Name must not be empty' })).toBe(true)
  })

  it('returns true when worktree branch has an error in create-worktree mode', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, gitMode: 'create-worktree', worktreeBranchError: 'Invalid branch' })).toBe(true)
  })

  it('ignores worktree branch error when mode is current', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, gitMode: 'current', worktreeBranchError: 'Invalid branch' })).toBe(false)
  })

  it('returns true when switch-branch mode has no branch selected', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, gitMode: 'switch-branch', checkoutBranch: '' })).toBe(true)
  })

  it('returns false when switch-branch mode has a branch selected', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, gitMode: 'switch-branch', checkoutBranch: 'main' })).toBe(false)
  })

  it('returns true when use-worktree mode has no path selected', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, gitMode: 'use-worktree', useWorktreePath: '' })).toBe(true)
  })

  it('returns false when use-worktree mode has a path selected', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, gitMode: 'use-worktree', useWorktreePath: '/path/to/wt' })).toBe(false)
  })
})

describe('isAgentCreateDisabled', () => {
  it('returns false when all fields are valid', () => {
    expect(isAgentCreateDisabled(validBase)).toBe(false)
  })

  it('returns true when submitting', () => {
    expect(isAgentCreateDisabled({ ...validBase, submitting: true })).toBe(true)
  })

  it('returns true when workerId is empty (no workers online)', () => {
    expect(isAgentCreateDisabled({ ...validBase, workerId: '' })).toBe(true)
  })

  it('returns true when workingDir is empty', () => {
    expect(isAgentCreateDisabled({ ...validBase, workingDir: '' })).toBe(true)
  })

  it('returns true when workingDir is whitespace-only', () => {
    expect(isAgentCreateDisabled({ ...validBase, workingDir: '  ' })).toBe(true)
  })

  it('returns true when worktree branch has an error in create-worktree mode', () => {
    expect(isAgentCreateDisabled({ ...validBase, gitMode: 'create-worktree', worktreeBranchError: 'Invalid branch' })).toBe(true)
  })

  it('ignores worktree branch error when mode is current', () => {
    expect(isAgentCreateDisabled({ ...validBase, gitMode: 'current', worktreeBranchError: 'err' })).toBe(false)
  })
})

describe('isTerminalCreateDisabled', () => {
  const valid = { ...validBase, shell: '/bin/bash' }

  it('returns false when all fields are valid', () => {
    expect(isTerminalCreateDisabled(valid)).toBe(false)
  })

  it('returns true when submitting', () => {
    expect(isTerminalCreateDisabled({ ...valid, submitting: true })).toBe(true)
  })

  it('returns true when workerId is empty (no workers online)', () => {
    expect(isTerminalCreateDisabled({ ...valid, workerId: '' })).toBe(true)
  })

  it('returns true when workingDir is empty', () => {
    expect(isTerminalCreateDisabled({ ...valid, workingDir: '' })).toBe(true)
  })

  it('returns true when workingDir is whitespace-only', () => {
    expect(isTerminalCreateDisabled({ ...valid, workingDir: '\t' })).toBe(true)
  })

  it('returns true when shell is empty', () => {
    expect(isTerminalCreateDisabled({ ...valid, shell: '' })).toBe(true)
  })

  it('returns true when worktree branch has an error in create-worktree mode', () => {
    expect(isTerminalCreateDisabled({ ...valid, gitMode: 'create-worktree', worktreeBranchError: 'err' })).toBe(true)
  })

  it('ignores worktree branch error when mode is current', () => {
    expect(isTerminalCreateDisabled({ ...valid, gitMode: 'current', worktreeBranchError: 'err' })).toBe(false)
  })
})
