import { describe, expect, it } from 'vitest'
import { codexCommandFromItem, codexUnwrapCommand } from './commandExecution'

describe('codexUnwrapCommand', () => {
  it('strips /bin/zsh -lc shell wrapper', () => {
    expect(codexUnwrapCommand('/bin/zsh -lc \'echo hi\'')).toBe('echo hi')
  })

  it('passes through unwrapped commands', () => {
    expect(codexUnwrapCommand('echo hi')).toBe('echo hi')
  })
})

describe('codexCommandFromItem', () => {
  it('returns null for non-commandExecution items', () => {
    expect(codexCommandFromItem(null)).toBeNull()
    expect(codexCommandFromItem({ type: 'agentMessage' })).toBeNull()
  })

  it('extracts the structured payload', () => {
    expect(codexCommandFromItem({
      type: 'commandExecution',
      command: 'echo hi',
      aggregatedOutput: 'hi',
      exitCode: 0,
      durationMs: 10,
      status: 'completed',
    })).toEqual({
      output: 'hi',
      exitCode: 0,
      durationMs: 10,
      isError: false,
    })
  })

  it('marks isError when status=failed', () => {
    const source = codexCommandFromItem({
      type: 'commandExecution',
      aggregatedOutput: '',
      status: 'failed',
    })
    expect(source?.isError).toBe(true)
  })

  it('marks isError when exit code is non-zero', () => {
    const source = codexCommandFromItem({
      type: 'commandExecution',
      aggregatedOutput: '',
      exitCode: 5,
      status: 'completed',
    })
    expect(source?.isError).toBe(true)
    expect(source?.exitCode).toBe(5)
  })
})
