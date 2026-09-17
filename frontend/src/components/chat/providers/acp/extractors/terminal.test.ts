import { describe, expect, it } from 'vitest'
import { acpTerminalIds, acpTerminalResults } from './terminal'

const TERMINAL = { type: 'terminal', terminalId: 'term-1' }

describe('acpTerminalIds', () => {
  it('collects each distinct terminal id once', () => {
    expect(acpTerminalIds([TERMINAL, TERMINAL, { type: 'content' }, 'x'])).toEqual(['term-1'])
  })

  it('answers nothing for content that is not a list', () => {
    expect(acpTerminalIds(undefined)).toEqual([])
    expect(acpTerminalIds({ type: 'terminal' })).toEqual([])
  })
})

describe('acpTerminalResults', () => {
  it('reads the output the supplement stored for each id the call names', () => {
    const { entries, unresolved } = acpTerminalResults(
      { content: [TERMINAL] },
      { sessionUpdate: 'tool_call', toolCallId: 'c', terminals: { 'term-1': { output: 'ok\n', exitCode: 0 } } },
    )
    expect(unresolved).toEqual([])
    expect(entries).toEqual([{ label: 'Terminal term-1', output: 'ok\n', exitCode: 0, truncated: false }])
  })

  it('reports a terminal the host no longer holds as unresolved', () => {
    expect(acpTerminalResults({ content: [TERMINAL] }, undefined).unresolved).toEqual(['term-1'])
  })

  // The AGENT chooses the ids. Looking one up in a plain object answers `toString`
  // with a function off `Object.prototype`, and the row then carried `output:
  // undefined` through a field the IR declares as `string` -- which the command body
  // dereferences and crashes on. Every entry this answers must carry real text.
  it.each(['toString', 'constructor', 'valueOf', 'hasOwnProperty', '__proto__'])('resolves nothing for the inherited id %s', (terminalId) => {
    const call = { content: [{ type: 'terminal', terminalId }] }
    for (const supplement of [undefined, { sessionUpdate: 'tool_call', toolCallId: 'c', terminals: { 'term-1': { output: 'ok' } } }]) {
      const { entries, unresolved } = acpTerminalResults(call, supplement)
      expect(unresolved).toEqual([terminalId])
      expect(entries).toEqual([])
    }
  })

  it('carries the signal that ended a process no exit code describes', () => {
    const { entries } = acpTerminalResults(
      { content: [TERMINAL] },
      { sessionUpdate: 'tool_call', toolCallId: 'c', terminals: { 'term-1': { output: '', signal: 'killed' } } },
    )
    expect(entries[0]).toEqual({ label: 'Terminal term-1', output: '', truncated: false, signal: 'killed' })
  })
})
