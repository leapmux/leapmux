import { describe, expect, it } from 'vitest'
import { gooseTerminalOutputFileLimit } from './terminalOutputLimit'

function fixture() {
  const original = { sessionUpdate: 'tool_call_update', toolCallId: 'call', status: 'completed' }
  const reference = { type: 'terminal', terminalId: 'terminal' }
  const protocol = { _meta: { goose: { toolCall: { toolName: 'shell', extensionName: 'developer' } } }, content: [reference] }
  const provider = { ...original, protocol, terminals: { terminal: { output: 'native retained tail42', truncated: true, exitCode: 0 } } }
  return { original, provider, reference, supplemental: { provider } }
}

describe('gooseTerminalOutputFileLimit', () => {
  it('reads the captured native terminal reference and its limited bytes', () => {
    const f = fixture()
    expect(gooseTerminalOutputFileLimit(f.original, f.supplemental, 'call')).toEqual({ callId: 'call', terminalId: 'terminal', text: 'native retained tail42' })
  })

  it('rejects another original or supplement identity', () => {
    const f = fixture()
    expect(() => gooseTerminalOutputFileLimit(f.original, f.supplemental, 'foreign')).toThrow('completed')
    expect(() => gooseTerminalOutputFileLimit(f.original, { provider: { ...f.provider, toolCallId: 'foreign' } }, 'call')).toThrow('another')
    expect(() => gooseTerminalOutputFileLimit(f.original, undefined, 'call')).toThrow('record')
  })

  it('rejects another tool or terminal reference', () => {
    const f = fixture()
    f.provider.protocol._meta.goose.toolCall.toolName = 'other'
    expect(() => gooseTerminalOutputFileLimit(f.original, f.supplemental, 'call')).toThrow('reference')
    const missing = fixture()
    missing.reference.terminalId = 'foreign'
    expect(() => gooseTerminalOutputFileLimit(missing.original, missing.supplemental, 'call')).toThrow('reference')
  })

  it.each([
    { output: null, truncated: true, exitCode: 0 },
    { output: 'tail', truncated: false, exitCode: 0 },
    { output: 'tail', truncated: true, exitCode: 1 },
    { output: 'tail', truncated: true, exitCode: null },
  ])('rejects another retained output or exit state: %j', (terminal) => {
    const f = fixture()
    expect(() => gooseTerminalOutputFileLimit(f.original, { provider: { ...f.provider, terminals: { terminal } } }, 'call')).toThrow('exit state')
  })
})
