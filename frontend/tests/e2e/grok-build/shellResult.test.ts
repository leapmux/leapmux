import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { readGrokShellResult } from './shellResult'

function request(content: string, callId = 'actual-grok-shell'): MockModelRequestRecord {
  return {
    protocol: 'openai-chat-completions',
    path: '/v1/chat/completions',
    body: { messages: [
      { role: 'assistant', tool_calls: [{ id: callId, type: 'function', function: { name: 'Bash', arguments: '{"command":"printf CALCULATED42"}' } }] },
      { role: 'tool', tool_call_id: callId, content },
    ] },
  }
}

describe('readGrokShellResult', () => {
  it.each([
    { exitCode: 0, text: 'exit: 0\nSHELL0fe28069e7b0454d84a9cecc2291e38c42\n' },
    { exitCode: 7, text: 'exit: 7\nSHELLERR0fe28069e7b0454d84a9cecc2291e38c77\n' },
    { exitCode: -1, text: 'exit: -1\nACTUAL_FAILURE\n' },
    { exitCode: Number.MAX_SAFE_INTEGER, text: `exit: ${Number.MAX_SAFE_INTEGER}\nACTUAL_OUTPUT\n` },
  ])('retains the actual native exit $exitCode and every output byte', ({ exitCode, text }) => {
    expect(readGrokShellResult(request(text), 'actual-grok-shell')).toEqual({ text, exitCode, failed: exitCode !== 0 })
  })
  it('preserves an empty native output with explicit exit zero', () => {
    expect(readGrokShellResult(request('exit: 0\n'), 'actual-grok-shell')).toEqual({ text: 'exit: 0\n', exitCode: 0, failed: false })
  })
  it.each([
    '',
    'ACTUAL_OUTPUT\n',
    'ACTUAL_OUTPUT\nexit: 7\n',
    'exit: 0',
    'exit: unknown\n',
    'exit: 0.5\n',
    'exit: +7\n',
    'exit: 07\n',
    'exit: -0\n',
    'exit: 7 trailing\n',
    `exit: ${Number.MAX_SAFE_INTEGER + 1}\n`,
    `exit: ${'9'.repeat(400)}\n`,
    'exit: killed (timeout)\nPARTIAL_OUTPUT\n',
    '[Command moved to background]\nPARTIAL_OUTPUT\n',
  ])('refuses an absent or malformed completed native header: %j', (text) => {
    expect(() => readGrokShellResult(request(text), 'actual-grok-shell')).toThrow('complete safe integer exit header')
  })
  it('keeps an exit-looking output line without replacing the native first-line status', () => {
    const text = 'exit: 0\nexit: 7\n'
    expect(readGrokShellResult(request(text), 'actual-grok-shell')).toEqual({ text, exitCode: 0, failed: false })
  })
  it('keeps an exit-looking command output line opaque after the first native header', () => {
    const text = 'exit: 0\nexit: 7\nACTUAL_OUTPUT\n'
    expect(readGrokShellResult(request(text), 'actual-grok-shell')).toEqual({ text, exitCode: 0, failed: false })
  })
  it('does not read native exit metadata from the scripted command arguments', () => {
    const native: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { messages: [
      { role: 'assistant', tool_calls: [{ id: 'actual-grok-shell', type: 'function', function: { name: 'Bash', arguments: '{"command":"printf \'exit: 7\\n\'"}' } }] },
      { role: 'tool', tool_call_id: 'actual-grok-shell', content: 'ACTUAL_OUTPUT\n' },
    ] } }
    expect(() => readGrokShellResult(native, 'actual-grok-shell')).toThrow('complete safe integer exit header')
  })
  it('refuses a result from another native call even when its output contains a valid header', () => {
    expect(() => readGrokShellResult(request('exit: 7\nACTUAL_FAILURE\n', 'another-call'), 'actual-grok-shell')).toThrow('0 results')
  })
  it('refuses duplicate results for the same exact native call', () => {
    const native: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { messages: [
      { role: 'tool', tool_call_id: 'actual-grok-shell', content: 'exit: 0\nFIRST42\n' },
      { role: 'tool', tool_call_id: 'actual-grok-shell', content: 'exit: 7\nSECOND77\n' },
    ] } }
    expect(() => readGrokShellResult(native, 'actual-grok-shell')).toThrow('2 results')
  })
  it('refuses an empty native call identity before reading any result', () => {
    expect(() => readGrokShellResult(request('exit: 0\n', ''), '')).toThrow('nonempty call ID')
  })
})
