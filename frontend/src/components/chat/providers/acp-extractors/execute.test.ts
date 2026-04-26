import { describe, expect, it } from 'vitest'
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
      isError: false,
    })
  })

  it('marks isError when status=failed', () => {
    const source = acpExecuteFromToolCall({
      kind: 'execute',
      status: 'failed',
      content: [{ type: 'content', content: { text: 'oops' } }],
    })
    expect(source?.isError).toBe(true)
    expect(source?.exitCode).toBeNull()
  })

  it('marks isError when exit code is non-zero', () => {
    const source = acpExecuteFromToolCall({
      kind: 'execute',
      status: 'completed',
      rawOutput: { metadata: { exit: 5 } },
      content: [],
    })
    expect(source?.isError).toBe(true)
    expect(source?.exitCode).toBe(5)
  })
})
