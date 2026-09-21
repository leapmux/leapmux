import { describe, expect, it } from 'vitest'
import { codexCommandActionsFromItem, codexCommandFromItem, codexUnwrapCommand } from './execute'

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
    })
  })

  it('marks isError when status=failed', () => {
    const source = codexCommandFromItem({
      type: 'commandExecution',
      aggregatedOutput: '',
      status: 'failed',
    })
    expect(source).not.toBeNull()
  })

  it('carries a non-zero exit code for the shared label to read', () => {
    const source = codexCommandFromItem({
      type: 'commandExecution',
      aggregatedOutput: '',
      exitCode: 5,
      status: 'completed',
    })
    expect(source?.exitCode).toBe(5)
  })

  // A refused approval never started a process. The row carries the refusal in
  // its status word, and the source claims neither an error nor an exit code.
  it('carries no exit code for a refused approval', () => {
    const source = codexCommandFromItem({
      type: 'commandExecution',
      command: './deploy.sh production',
      aggregatedOutput: '',
      status: 'declined',
    })
    expect(source?.exitCode).toBeNull()
  })
})

describe('codexCommandActionsFromItem', () => {
  it('translates every current command action', () => {
    expect(codexCommandActionsFromItem({
      commandActions: [
        { type: 'read', command: 'sed -n \'1,5p\' src/main.ts', name: 'main.ts', path: '/repo/src/main.ts' },
        { type: 'listFiles', command: 'rg --files src', path: 'src' },
        { type: 'search', command: 'rg -n \'needle\' src', query: 'needle', path: 'src' },
        { type: 'unknown', command: 'npm run custom-task' },
      ],
    })).toEqual([
      { kind: 'read', command: 'sed -n \'1,5p\' src/main.ts', name: 'main.ts', path: '/repo/src/main.ts' },
      { kind: 'list', command: 'rg --files src', path: 'src' },
      { kind: 'search', command: 'rg -n \'needle\' src', query: 'needle', path: 'src' },
      { kind: 'unknown', command: 'npm run custom-task' },
    ])
  })

  it('keeps nullable properties absent and preserves a future type as unknown', () => {
    expect(codexCommandActionsFromItem({
      commandActions: [
        { type: 'listFiles', command: 'pwd', path: null },
        { type: 'search', command: 'rg --files', query: null, path: null },
        { type: 'futureAction', command: 'future --flag', detail: 'new' },
      ],
    })).toEqual([
      { kind: 'list', command: 'pwd' },
      { kind: 'search', command: 'rg --files' },
      { kind: 'unknown', command: 'future --flag' },
    ])
  })

  it('drops entries without a command and returns no actions for an invalid list', () => {
    expect(codexCommandActionsFromItem({ commandActions: [null, 'read', {}, { type: 'read', path: '/repo/a.ts' }] })).toEqual([])
    expect(codexCommandActionsFromItem({ commandActions: 'not-an-array' })).toEqual([])
    expect(codexCommandActionsFromItem({})).toEqual([])
  })
})
