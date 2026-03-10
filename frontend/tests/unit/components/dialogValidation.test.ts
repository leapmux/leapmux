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
  createWorktree: false,
  worktreeBranchError: null,
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

  it('returns true when worktree branch has an error', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, createWorktree: true, worktreeBranchError: 'Invalid branch' })).toBe(true)
  })

  it('ignores worktree branch error when worktree is not enabled', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, createWorktree: false, worktreeBranchError: 'Invalid branch' })).toBe(false)
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

  it('returns true when worktree branch has an error', () => {
    expect(isAgentCreateDisabled({ ...validBase, createWorktree: true, worktreeBranchError: 'Invalid branch' })).toBe(true)
  })

  it('ignores worktree branch error when worktree is not enabled', () => {
    expect(isAgentCreateDisabled({ ...validBase, createWorktree: false, worktreeBranchError: 'err' })).toBe(false)
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

  it('returns true when worktree branch has an error', () => {
    expect(isTerminalCreateDisabled({ ...valid, createWorktree: true, worktreeBranchError: 'err' })).toBe(true)
  })

  it('ignores worktree branch error when worktree is not enabled', () => {
    expect(isTerminalCreateDisabled({ ...valid, createWorktree: false, worktreeBranchError: 'err' })).toBe(false)
  })
})
