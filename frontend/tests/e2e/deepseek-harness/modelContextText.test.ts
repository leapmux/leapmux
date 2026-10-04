import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { deepseekHarnessModelContextText } from './modelContextText'

function request(extra: Record<string, unknown> = {}): MockModelRequestRecord {
  return {
    protocol: 'anthropic-messages',
    path: '/v1/messages',
    body: {
      system: [{ type: 'text', text: 'ACTUAL_SYSTEM_INSTRUCTION' }],
      messages: [
        { role: 'user', content: [{ type: 'text', text: 'ACTUAL_PRIOR_USER_PROMPT' }] },
        { role: 'assistant', content: [{ type: 'text', text: 'ACTUAL_PRIOR_ASSISTANT_ANSWER' }] },
        { role: 'user', content: [{ type: 'text', text: 'ACTUAL_NEXT_USER_PROMPT' }] },
      ],
      ...extra,
    },
  }
}

describe('deepseekHarnessModelContextText', () => {
  it.each([
    { dsh_session_log: { version: 1, sessionFormatVersion: 4, session: { version: 4, id: 'native-session', createdAt: 1, cwd: '/private/native-workspace', agentPreset: 'default' }, afterSeq: 0, throughSeq: 16, events: [{ seq: 16, time: 1, type: 'assistant/message', surfaceOp: 'append', data: { message: { content: [{ type: 'text', text: 'FORENSIC_ONLY_CONTEXT' }] } } }] } },
    { dsh_plugin_packages: { version: 1, packages: [{ name: 'FORENSIC_ONLY_CONTEXT', version: '1' }] } },
    { tools: [{ name: 'native_tool', description: 'FORENSIC_ONLY_CONTEXT', input_schema: { type: 'object', properties: {} } }] },
  ])('excludes forensic metadata and offered schemas: %j', (extra) => {
    const record = request(extra)
    const before = structuredClone(record)
    expect(nativeModelContextText(record)).toContain('FORENSIC_ONLY_CONTEXT')
    const text = deepseekHarnessModelContextText(record)
    expect(text).toContain('ACTUAL_PRIOR_USER_PROMPT')
    expect(text).toContain('ACTUAL_PRIOR_ASSISTANT_ANSWER')
    expect(text).toContain('ACTUAL_NEXT_USER_PROMPT')
    expect(text).toContain('ACTUAL_SYSTEM_INSTRUCTION')
    expect(text).not.toContain('FORENSIC_ONLY_CONTEXT')
    expect(record).toEqual(before)
  })

  it('excludes unrelated server-held context while preserving the generic reader', () => {
    const record = request()
    record.serverContext = { conversationId: 'another-provider-session', messages: [{ role: 'assistant', content: 'UNRELATED_SERVER_CONTEXT' }] }
    expect(nativeModelContextText(record)).toContain('UNRELATED_SERVER_CONTEXT')
    expect(deepseekHarnessModelContextText(record)).not.toContain('UNRELATED_SERVER_CONTEXT')
  })

  it.each([
    { text: 'Objective: "clear"' },
    { text: 'The first line.\nThe second line.' },
    { text: 'The first field.\tThe second field.' },
    { text: '' },
  ])('preserves actual native text without JSON escapes: $text', ({ text }) => {
    const record: MockModelRequestRecord = { protocol: 'anthropic-messages', path: '/v1/messages', body: { system: [], messages: [{ role: 'user', content: [{ type: 'text', text }] }] } }
    expect(deepseekHarnessModelContextText(record)).toBe(text)
  })

  it('preserves native message order and actual tool result text', () => {
    const record = request({ system: 'System text.', messages: [
      { role: 'user', content: [{ type: 'text', text: 'First prompt.' }] },
      { role: 'assistant', content: [{ type: 'thinking', thinking: 'Actual reasoning.' }, { type: 'text', text: 'First answer.' }, { type: 'tool_use', name: 'native_tool', input: { quoted: 'ARGUMENT_ONLY' } }] },
      { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'native-call', content: [{ type: 'text', text: '0\tfalse' }, { type: 'image', source: { data: 'IMAGE_ONLY' } }] }] },
    ] })
    expect(deepseekHarnessModelContextText(record)).toBe('System text.\nFirst prompt.\nActual reasoning.First answer.\n0\tfalse')
  })

  it.each([null, false, 0, '', [], {}, { messages: null }, { messages: {} }].map(body => ({ body })))('rejects a missing native message array: $body', ({ body }) => {
    expect(() => deepseekHarnessModelContextText({ protocol: 'anthropic-messages', path: '/v1/messages', body })).toThrow('native message array')
  })

  it.each([null, false, 0, {}, { role: 'system', content: 'Invalid role.' }, { role: 'tool', content: 'Invalid role.' }])('rejects an invalid native message: %j', (message) => {
    expect(() => deepseekHarnessModelContextText(request({ messages: [message] }))).toThrow('invalid native message')
  })

  it.each([undefined, null, 0, {}, [{ type: 'text' }], [{ type: 'thinking' }], [{ type: 'unknown', text: 'Unknown native block.' }], [{ type: 'tool_result', content: [{ type: 'tool_result', content: [] }] }]].map(content => ({ content })))('rejects invalid native content: $content', ({ content }) => {
    expect(() => deepseekHarnessModelContextText(request({ messages: [{ role: 'user', content }] }))).toThrow('DeepSeek Harness')
  })

  it('rejects another model protocol and preserves an explicit empty conversation', () => {
    expect(() => deepseekHarnessModelContextText({ ...request(), protocol: 'openai-responses' })).toThrow('native Anthropic Messages request')
    expect(deepseekHarnessModelContextText(request({ system: [], messages: [] }))).toBe('')
  })
})
