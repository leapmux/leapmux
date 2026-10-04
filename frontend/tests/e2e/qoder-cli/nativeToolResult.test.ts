import { describe, expect, it } from 'vitest'
import { qoderNativeToolResult } from './nativeToolResult'

function frame(content: unknown, callId = 'call', isError = false): unknown {
  return { type: 'user', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: callId, content, is_error: isError }] } }
}

describe('qoderNativeToolResult', () => {
  it('reads the exact native result without a Worker span assumption', () => {
    expect(qoderNativeToolResult([frame('native text')], 'call').text).toBe('native text')
    expect(qoderNativeToolResult([frame([{ type: 'text', text: 'first' }, { type: 'text', text: 'last' }])], 'call').text).toBe('firstlast')
  })

  it('keeps empty native text', () => {
    expect(qoderNativeToolResult([frame('')], 'call').text).toBe('')
  })

  it('rejects a repeated, foreign, or failed result', () => {
    expect(() => qoderNativeToolResult([frame('a'), frame('b')], 'call')).toThrow('successful')
    expect(() => qoderNativeToolResult([frame('a', 'other')], 'call')).toThrow('successful')
    expect(() => qoderNativeToolResult([frame('a', 'call', true)], 'call')).toThrow('successful')
  })

  it.each([null, false, [], [{ type: 'image', data: 'not-text' }], [{ type: 'text', text: 0 }]].map(content => ({ content })))('rejects invalid or nontext content: %j', ({ content }) => {
    expect(() => qoderNativeToolResult([frame(content)], 'call')).toThrow('text')
  })
})
