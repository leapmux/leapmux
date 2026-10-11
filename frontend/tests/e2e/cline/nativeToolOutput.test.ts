import { describe, expect, it } from 'vitest'
import { clineNativeOutputLimit } from './nativeToolOutput'

describe('clineNativeOutputLimit', () => {
  const result = 'head\n[... output truncated: 200000 chars total. Refine the command ...]\ntail'
  it('retains only the actual successful command excerpt', () => {
    expect(clineNativeOutputLimit(JSON.stringify([{ query: 'command', success: true, result }]))).toBe(result)
  })

  it('reads the captured authoritative Worker operation when the model projection drops its notice', () => {
    const value = { version: 'v1', event: 'tool.finished', sessionId: 'session', payload: { toolCallId: 'call', toolName: 'run_commands', output: [{ query: 'command', success: true, result }] } }
    expect(clineNativeOutputLimit(JSON.stringify(value))).toBe(result)
    // A frame that is not the finished Worker operation is read as the model projection, which must be the operation list.
    expect(() => clineNativeOutputLimit(JSON.stringify({ ...value, event: 'tool.started' }))).toThrow('The Cline large command result has no exact successful native operation.')
  })

  it('refuses an absent notice, failed operation, or ambiguous command batch', () => {
    expect(() => clineNativeOutputLimit(JSON.stringify([{ success: true, result: 'whole output' }]))).toThrow('notice')
    expect(() => clineNativeOutputLimit(JSON.stringify([{ success: false, result }]))).toThrow('operation')
    expect(() => clineNativeOutputLimit(JSON.stringify([{ success: true, result }, { success: true, result }]))).toThrow('operation')
  })
})
