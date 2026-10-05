import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { kimiModelContextText } from './modelContextText'

function request(extra: Record<string, unknown> = {}): MockModelRequestRecord {
  return {
    protocol: 'openai-chat-completions',
    path: '/v1/chat/completions',
    body: {
      model: 'native-model',
      messages: [
        { role: 'system', content: 'ACTUAL_SYSTEM_INSTRUCTION' },
        { role: 'user', content: 'ACTUAL_PRIOR_USER_PROMPT' },
        { role: 'assistant', content: 'ACTUAL_PRIOR_ASSISTANT_ANSWER' },
        { role: 'assistant', content: null, tool_calls: [{ id: 'native-call' }] },
        { role: 'tool', content: 'ACTUAL_TOOL_OUTPUT' },
        { role: 'user', content: 'ACTUAL_NEXT_USER_PROMPT' },
      ],
      ...extra,
    },
  }
}

describe('kimiModelContextText', () => {
  it.each([
    { text: 'You are now in "agent swarm" mode.' },
    { text: 'The first line.\nThe second line.' },
    { text: 'The first field.\tThe second field.' },
  ])('preserves actual native text without JSON escapes: $text', ({ text }) => {
    const record = request({ messages: [{ role: 'user', content: text }] })
    expect(nativeModelContextText(record)).not.toBe(text)
    expect(kimiModelContextText(record)).toBe(text)
  })

  it('joins the native message text in order and skips a tool-call step', () => {
    const record = request()
    const before = structuredClone(record)
    expect(kimiModelContextText(record)).toBe(
      'ACTUAL_SYSTEM_INSTRUCTION\nACTUAL_PRIOR_USER_PROMPT\nACTUAL_PRIOR_ASSISTANT_ANSWER\nACTUAL_TOOL_OUTPUT\nACTUAL_NEXT_USER_PROMPT',
    )
    expect(record).toEqual(before)
  })

  it('excludes unrelated server-held context while preserving the generic reader', () => {
    const record = request()
    record.serverContext = {
      conversationId: 'unrelated-conversation',
      messages: [{ role: 'user', content: 'UNRELATED_SERVER_CONTEXT' }],
    }
    expect(nativeModelContextText(record)).toContain('UNRELATED_SERVER_CONTEXT')
    expect(kimiModelContextText(record)).not.toContain('UNRELATED_SERVER_CONTEXT')
  })

  it.each([null, false, 0, '', [], {}, { messages: null }, { messages: {} }].map(body => ({ body })))('rejects a missing native message array: $body', ({ body }) => {
    expect(() => kimiModelContextText({ protocol: 'openai-chat-completions', path: '/v1/chat/completions', body })).toThrow('native message array')
  })

  it.each([null, false, 0, {}, { content: 'No role.' }])('rejects an invalid native message: %j', (message) => {
    expect(() => kimiModelContextText(request({ messages: [message] }))).toThrow('invalid native message')
  })

  it.each([0, false, {}, [{ type: 'text', text: 'blocks' }]].map(content => ({ content })))('rejects non-text native content: $content', ({ content }) => {
    expect(() => kimiModelContextText(request({ messages: [{ role: 'user', content }] }))).toThrow('non-text native message')
  })

  it('rejects another model protocol and preserves an explicit empty conversation', () => {
    expect(() => kimiModelContextText({ ...request(), protocol: 'anthropic-messages' })).toThrow('native OpenAI chat completions request')
    expect(kimiModelContextText(request({ messages: [] }))).toBe('')
  })
})
