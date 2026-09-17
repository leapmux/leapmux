import { describe, expect, it } from 'vitest'
import { commandStatusLabel } from '../../../ir/commandResult'
import { acpExecuteFromToolCall } from './execute'

describe('acpExecuteFromToolCall', () => {
  it('returns null for null/undefined toolUse', () => {
    expect(acpExecuteFromToolCall(null)).toBeNull()
    expect(acpExecuteFromToolCall(undefined)).toBeNull()
  })

  it('extracts text from content array, exit code from metadata', () => {
    const source = acpExecuteFromToolCall({
      kind: 'execute',
      status: 'completed',
      rawInput: { command: 'echo hi' },
      rawOutput: { metadata: { exit: 0 } },
      content: [{ type: 'content', content: { text: 'hi\n' } }],
    })
    expect(source).toEqual({
      output: 'hi\n',
      exitCode: 0,
      truncated: false,
    })
  })

  // The error words live on the row's status; the source keeps only what the
  // command itself reported, and the shared label reads the exit code.
  it('carries the exit code the raw output metadata states', () => {
    const source = acpExecuteFromToolCall({
      kind: 'execute',
      status: 'completed',
      rawOutput: { metadata: { exit: 5 } },
      content: [],
    })
    expect(source?.exitCode).toBe(5)
    expect(commandStatusLabel('completed', { ...(source?.exitCode !== undefined ? { exitCode: source.exitCode } : {}) })).toBe('Error (exit 5)')
  })

  it('still extracts text when a terminal content block is also present', () => {
    const source = acpExecuteFromToolCall({
      kind: 'execute',
      status: 'completed',
      rawInput: { command: 'echo hi' },
      rawOutput: { metadata: { exit: 0 } },
      content: [
        { type: 'terminal', terminalId: 'term_abc' },
        { type: 'content', content: { text: 'hi\n' } },
      ],
    })
    expect(source).toEqual({
      output: 'hi\n',
      exitCode: 0,
      truncated: false,
    })
  })
})
