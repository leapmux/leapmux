import type { MockModelRequestRecord } from './mockModelScript'
import { describe, expect, it } from 'vitest'
import { nativeToolResult } from './nativeToolResult'

function request(protocol: MockModelRequestRecord['protocol'], body: unknown): MockModelRequestRecord {
  return { protocol, path: '/mock', body }
}

describe('nativeToolResult', () => {
  it('reads a Chat Completions tool result instead of its question options', () => {
    const record = request('openai-chat-completions', {
      messages: [
        { role: 'assistant', tool_calls: [{ id: 'color', function: { arguments: '{"options":["Blue","Green"]}' } }] },
        { role: 'tool', tool_call_id: 'color', content: '{"answer":"Green"}' },
      ],
    })
    expect(nativeToolResult(record, 'color')).toBe('{"answer":"Green"}')
    expect(nativeToolResult(record, 'color')).not.toContain('Blue')
  })

  it('reads a Responses function-call output instead of its arguments', () => {
    const record = request('openai-responses', {
      input: [
        { type: 'function_call', call_id: 'color', arguments: '{"options":["Blue","Red"]}' },
        { type: 'function_call_output', call_id: 'color', output: '{"answer":"Red"}' },
      ],
    })
    expect(nativeToolResult(record, 'color')).toBe('{"answer":"Red"}')
  })

  it('reads an Anthropic tool-result block instead of its tool-use input', () => {
    const record = request('anthropic-messages', {
      messages: [
        { role: 'assistant', content: [{ type: 'tool_use', id: 'color', input: { options: ['Blue', 'Green'] } }] },
        { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'color', content: [{ type: 'text', text: 'Green' }] }] },
      ],
    })
    expect(nativeToolResult(record, 'color')).toContain('Green')
    expect(nativeToolResult(record, 'color')).not.toContain('Blue')
  })

  it('rejects a missing, mismatched, or duplicate result', () => {
    const record = request('openai-chat-completions', {
      messages: [{ role: 'tool', tool_call_id: 'color', content: 'Green' }],
    })
    expect(() => nativeToolResult(record, 'other')).toThrow('received 0')
    expect(() => nativeToolResult(undefined, 'color')).toThrow('no native model request')
    const duplicate = request('openai-chat-completions', {
      messages: [
        { role: 'tool', tool_call_id: 'color', content: 'Green' },
        { role: 'tool', tool_call_id: 'color', content: 'Blue' },
      ],
    })
    expect(() => nativeToolResult(duplicate, 'color')).toThrow('received 2')
  })

  it('rejects a result without content or an unsupported protocol', () => {
    expect(() => nativeToolResult(request('openai-chat-completions', {
      messages: [{ role: 'tool', tool_call_id: 'color' }],
    }), 'color')).toThrow('has no content')
    expect(() => nativeToolResult(request('aws-event-stream', {}), 'color')).toThrow('unavailable')
  })
})
