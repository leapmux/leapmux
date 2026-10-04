import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { gooseNativeOutput } from './nativeToolOutput'

const path = resolve('native-full-output-fixture', '.tmpNative', 'stdout-0')
const notice = `[Output exceeded 2000 line limit (6001 lines total). Full output saved to ${path}. Read it with shell commands like head, tail, or sed up to 2000 lines at a time.]`
function frame(overrides: Record<string, unknown> = {}): unknown {
  return { sessionUpdate: 'tool_call_update', toolCallId: 'call', status: 'completed', rawOutput: { stdout: 'tail', stderr: '', exit_code: 0 }, content: [{ type: 'content', content: { type: 'text', text: notice } }], _meta: { goose: { toolCall: { toolName: 'shell', extensionName: 'developer' } } }, ...overrides }
}

describe('gooseNativeOutput', () => {
  it('reads the stdout slot from the native notice rather than a command argument', () => {
    expect(gooseNativeOutput([frame()], 'call')).toEqual({ path, excerpt: 'tail' })
  })

  it('ignores a separate interleaved slot while retaining one stdout reference', () => {
    const extra = { type: 'content', content: { type: 'text', text: notice.replace('stdout-0', 'output-0') } }
    expect(gooseNativeOutput([frame({ content: [{ type: 'content', content: { type: 'text', text: notice } }, extra] })], 'call').path).toBe(path)
  })

  it.each([
    { status: 'failed' },
    { toolCallId: 'other' },
    { rawOutput: { stdout: 'tail', stderr: '', exit_code: 1 } },
    { rawOutput: { stdout: 'tail', stderr: 'error', exit_code: 0 } },
    { rawOutput: { stdout: null, stderr: '', exit_code: 0 } },
    { content: null },
    { _meta: { goose: { toolCall: { toolName: 'other', extensionName: 'developer' } } } },
  ])('rejects a different or invalid native shell result %j', (overrides) => {
    expect(() => gooseNativeOutput([frame(overrides)], 'call')).toThrow('result')
  })

  it.each(['head', notice + notice, notice.replace('stdout-0', 'stdout-8'), notice.replace('.tmpNative', 'other'), notice.replace(path, 'stdout-0')])('rejects a missing, repeated, or different slot notice %j', (text) => {
    expect(() => gooseNativeOutput([frame({ content: [{ type: 'content', content: { type: 'text', text } }] })], 'call')).toThrow('reference')
  })
})
