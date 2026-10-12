import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { deepseekHarnessToolResultText } from './nativeToolResultText'

function request(callId: string, content: unknown): MockModelRequestRecord {
  return {
    protocol: 'anthropic-messages',
    path: '/v1/messages',
    body: {
      messages: [{
        role: 'user',
        content: [
          { type: 'tool_result', tool_use_id: 'another-native-call', content: [{ type: 'text', text: 'WRONG_CALL_ONLY' }] },
          { type: 'tool_result', tool_use_id: callId, content },
        ],
      }],
    },
  }
}

describe('deepseekHarnessToolResultText', () => {
  it('reads the exact chosen answer JSON inside the native question text block', () => {
    const answer = '{"answers":[{"id":"route","selected":["Second"]}]}'
    const record = request('deepseek-question-native', [{ type: 'text', text: answer }])
    expect(nativeToolResult(record, 'deepseek-question-native')).toBe(JSON.stringify([{ type: 'text', text: answer }]))
    expect(JSON.parse(deepseekHarnessToolResultText(record, 'deepseek-question-native'))).toEqual({ answers: [{ id: 'route', selected: ['Second'] }] })
  })

  it('reads the exact native MCP inspect text with zero and false values', () => {
    const result = 'NATIVE_MCP_INSPECT:{"count":0,"enabled":false,"text":"Native MCP input."}'
    const record = request('deepseek-mcp-native', [{ type: 'text', text: result }])
    expect(nativeToolResult(record, 'deepseek-mcp-native')).toBe(JSON.stringify([{ type: 'text', text: result }]))
    expect(deepseekHarnessToolResultText(record, 'deepseek-mcp-native')).toBe(result)
  })

  it('preserves an explicit empty native text value without mutating the request', () => {
    const record = request('empty-native-text', [{ type: 'text', text: '' }])
    const before = structuredClone(record)
    expect(deepseekHarnessToolResultText(record, 'empty-native-text')).toBe('')
    expect(record).toEqual(before)
  })

  it.each([
    [],
    [{ type: 'text', text: 'First' }, { type: 'text', text: 'Second' }],
    [{ type: 'text', text: 'Text' }, { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'AA==' } }],
    [{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'AA==' } }],
    [{ type: 'text' }],
    [{ type: 'text', text: null }],
    [{ type: 'text', text: 0 }],
    [{ type: 'text', text: false }],
    [{ text: 'No native block type' }],
    '[{"type":"text","text":"A scalar that imitates the native array"}]',
    { type: 'text', text: 'A block outside its native array' },
  ].map(content => ({ content })))('rejects content that is not one native text block: $content', ({ content }) => {
    expect(() => deepseekHarnessToolResultText(request('invalid-native-text', content), 'invalid-native-text')).toThrow('exactly one native text block')
  })

  it.each([undefined, null])('rejects absent native result content: %j', (content) => {
    expect(() => deepseekHarnessToolResultText(request('absent-native-text', content), 'absent-native-text')).toThrow('has no content')
  })

  it('rejects a missing request and a result for another call', () => {
    expect(() => deepseekHarnessToolResultText(undefined, 'missing-native-call')).toThrow('No native model request')
    expect(() => deepseekHarnessToolResultText(request('actual-native-call', [{ type: 'text', text: 'Actual text' }]), 'missing-native-call')).toThrow('contains 0 results')
  })

  it('rejects an ambiguous native result instead of choosing either answer', () => {
    const record: MockModelRequestRecord = {
      protocol: 'anthropic-messages',
      path: '/v1/messages',
      body: { messages: [{ role: 'user', content: [
        { type: 'tool_result', tool_use_id: 'duplicate-native-call', content: [{ type: 'text', text: 'First' }] },
        { type: 'tool_result', tool_use_id: 'duplicate-native-call', content: [{ type: 'text', text: 'Second' }] },
      ] }] },
    }
    expect(() => deepseekHarnessToolResultText(record, 'duplicate-native-call')).toThrow('contains 2 results')
  })

  it('rejects another model protocol without reinterpreting its result', () => {
    const record: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { messages: [{ role: 'tool', tool_call_id: 'other-model-call', content: '[{"type":"text","text":"Other model output"}]' }] } }
    expect(() => deepseekHarnessToolResultText(record, 'other-model-call')).toThrow('native Anthropic Messages request')
  })
})
