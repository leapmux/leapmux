import type { CodebuddyToolFacts } from './toolCommon'
import { describe, expect, it } from 'vitest'
import { SYNTHETIC_TOOL_LIFECYCLE } from '../../../model/toolCallLifecycle'
import { codebuddyToolCall } from './toolCommon'

const script = 'return 40 + 2'

function facts(change: Partial<CodebuddyToolFacts> = {}): CodebuddyToolFacts {
  return { callId: 'native-repl', toolName: 'REPL', args: { code: script }, resultText: '', isError: false, lifecycle: { ...SYNTHETIC_TOOL_LIFECYCLE }, ...change }
}

describe('codebuddyToolCall', () => {
  it('reads the native JavaScript source from the exact REPL request', () => {
    const call = codebuddyToolCall(facts({ lifecycle: { ...SYNTHETIC_TOOL_LIFECYCLE, frameStatus: 'in_progress', rowFinal: false, resultFrameLanded: false } }))
    expect(call.kind).toBe('execute')
    if (call.kind !== 'execute')
      throw new Error('The native REPL request must use the execute model.')
    expect(call.request).toMatchObject({ command: script, language: 'javascript' })
    expect(call.status).toBe('in_progress')
  })

  it.each([null, 0, false, ''])('preserves a successful native result value: %j', (result) => {
    const text = JSON.stringify({ stdout: '', stderr: '', result })
    const call = codebuddyToolCall(facts({ resultText: text }))
    expect(call.status).toBe('completed')
    expect(call.result).toEqual({ unparsed: true, text })
  })

  it.each(['Error: computed-77', ''])('marks an exact native script error as failed without an outer error flag: %j', (error) => {
    const text = JSON.stringify({ stdout: '', stderr: '', error })
    const call = codebuddyToolCall(facts({ resultText: text }))
    expect(call.kind).toBe('execute')
    expect(call.status).toBe('failed')
    expect(call.result).toEqual({ failure: true, text })
    expect(call.degradation).toBeUndefined()
  })

  it.each([
    'Ordinary text that mentions error.',
    '{broken',
    JSON.stringify({ error: 'a document field' }),
    JSON.stringify({ stdout: 0, stderr: '', error: 'wrong stdout type' }),
    JSON.stringify({ stdout: '', stderr: null, error: 'wrong stderr type' }),
    JSON.stringify({ stdout: '', stderr: '', error: { message: 'wrong error type' } }),
  ])('keeps an incomplete or invalid native envelope unparsed: %j', (text) => {
    const call = codebuddyToolCall(facts({ resultText: text }))
    expect(call.status).toBe('completed')
    expect(call.result).toEqual({ unparsed: true, text })
  })

  it('does not infer failure from an unrelated tool that returns the same JSON fields', () => {
    const text = JSON.stringify({ stdout: '', stderr: '', error: 'a file data field' })
    const call = codebuddyToolCall(facts({ toolName: 'Read', args: { file_path: '/work/data.json' }, resultText: text }))
    expect(call.kind).toBe('read')
    expect(call.status).toBe('completed')
    expect(call.result).toEqual({ unparsed: true, text })
  })

  it('keeps an explicit retained cancellation when a native error result also arrives', () => {
    const text = JSON.stringify({ stdout: '', stderr: '', error: 'native failure after cancel' })
    const call = codebuddyToolCall(facts({ resultText: text, lifecycle: { ...SYNTHETIC_TOOL_LIFECYCLE, retainedOutcome: 'interrupted' } }))
    expect(call.status).toBe('cancelled')
  })

  it('keeps a failed shell call in the execute model without a fallback degradation', () => {
    const call = codebuddyToolCall(facts({ toolName: 'Bash', args: { command: 'exit 7' }, resultText: 'The native command failed.', isError: true }))
    expect(call.kind).toBe('execute')
    expect(call.status).toBe('failed')
    expect(call.degradation).toBeUndefined()
  })

  it('draws a shell result as a command with the stated exit', () => {
    const call = codebuddyToolCall(facts({ toolName: 'Bash', args: { command: 'exit 7' }, resultText: 'record', commandExit: { exitCode: 7 } }))
    expect(call.result).toStrictEqual({ commands: [{ output: 'record', exitCode: 7 }], unresolvedTerminals: [] })
  })

  it('keeps a landed shell result with empty text as a command that printed nothing', () => {
    const call = codebuddyToolCall(facts({ toolName: 'Bash', args: { command: 'true' }, resultText: '' }))
    expect(call.result).toStrictEqual({ commands: [{ output: '' }], unresolvedTerminals: [] })
  })

  it('states no shell result while the command runs', () => {
    const call = codebuddyToolCall(facts({ toolName: 'Bash', args: { command: 'sleep 9' }, lifecycle: { ...SYNTHETIC_TOOL_LIFECYCLE, frameStatus: 'in_progress', rowFinal: false, resultFrameLanded: false } }))
    expect(call.result).toBeUndefined()
  })
})
