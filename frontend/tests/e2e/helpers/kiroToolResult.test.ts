import type { MockModelRequestRecord } from './mockModelScript'
import { describe, expect, it } from 'vitest'
import { kiroToolResult } from './kiroToolResult'

function request(toolResults: unknown[]): MockModelRequestRecord {
  return {
    protocol: 'aws-event-stream',
    path: '/',
    body: { conversationState: { currentMessage: { userInputMessage: {
      content: 'REQUEST_ONLY_MARKER exit 7',
      userInputMessageContext: { toolResults },
    } } } },
  }
}

describe('kiroToolResult', () => {
  it('reads only the current native result for the requested call', () => {
    const actual = 'Output:\nACTUAL_STDERR_MARKER\n\nExit Code: 7'
    const result = kiroToolResult(request([
      { toolUseId: 'unrelated', content: [{ text: 'UNRELATED_RESULT' }], status: 'success' },
      { toolUseId: 'native-call', content: [{ text: actual }], status: 'error' },
    ]), 'native-call')
    expect(result).toEqual({ text: actual, failed: true, exitCode: 7 })
    expect(result.text).not.toContain('REQUEST_ONLY_MARKER')
    expect(result.text).not.toContain('UNRELATED_RESULT')
  })

  it('preserves an empty native result and explicit success', () => {
    expect(kiroToolResult(request([{ toolUseId: 'native-call', content: [{ text: '' }], status: 'success' }]), 'native-call'))
      .toEqual({ text: '', failed: false })
  })

  it('preserves a structured zero exit code', () => {
    expect(kiroToolResult(request([{ toolUseId: 'native-call', content: [{ json: { output: '', exitCode: 0 } }] }]), 'native-call'))
      .toEqual({ text: '{"output":"","exitCode":0}', exitCode: 0 })
  })

  it('keeps native status fields absent when the result states none', () => {
    expect(kiroToolResult(request([{ toolUseId: 'native-call', content: [{ text: 'Native result.' }] }]), 'native-call'))
      .toEqual({ text: 'Native result.' })
  })

  it.each([{ rows: [] }, { rows: [{ toolUseId: 'other', content: [{ text: 'Other.' }] }] }])('refuses a missing native call result: %j', ({ rows }) => {
    expect(() => kiroToolResult(request(rows), 'native-call')).toThrow('no unique native result')
  })

  it('refuses conflicting native exit codes', () => {
    expect(() => kiroToolResult(request([{ toolUseId: 'native-call', content: [{ text: 'Exit Code: 7' }, { json: { exitCode: 0 } }] }]), 'native-call'))
      .toThrow('conflicting native exit codes')
  })

  it.each([{ content: [] }, { content: [null] }, { content: [{ unsupported: true }] }])('refuses missing or malformed native content: %j', ({ content }) => {
    expect(() => kiroToolResult(request([{ toolUseId: 'native-call', content }]), 'native-call')).toThrow(/no native tool content|invalid native content block|no text or JSON/)
  })
})
