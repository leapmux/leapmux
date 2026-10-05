import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { kiroModelTurns } from './modelTurns'

function request(conversationState: unknown): MockModelRequestRecord {
  return { protocol: 'aws-event-stream', path: '/generateAssistantResponse', body: { conversationState } }
}

describe('kiroModelTurns', () => {
  it('reads the history in order, with assistant tool inputs, and the current prompt last', () => {
    const turns = kiroModelTurns(request({
      conversationId: 'native-conversation',
      history: [
        { userInputMessage: { content: 'You are Kiro.', origin: 'AI_EDITOR' } },
        { assistantResponseMessage: { content: 'I will follow these instructions.' } },
        { userInputMessage: { content: 'ORIGINAL_PROMPT' } },
        { assistantResponseMessage: { content: 'ORIGINAL_ANSWER', toolUses: [{ toolUseId: 'use', name: 'answer', input: { text: 'TOOL_INPUT' } }, 'not a tool use'] } },
        { userInputMessage: { content: '', userInputMessageContext: { toolResults: [{ content: [{ text: 'RESULT_ONLY' }] }] } } },
        { unknownEntry: { content: 'UNKNOWN_ONLY' } },
        'not an entry',
      ],
      currentMessage: { userInputMessage: { content: 'RESUMED_PROMPT' } },
    }))
    expect(turns).toEqual([
      { role: 'user', text: 'You are Kiro.' },
      { role: 'assistant', text: 'I will follow these instructions.' },
      { role: 'user', text: 'ORIGINAL_PROMPT' },
      { role: 'assistant', text: 'ORIGINAL_ANSWER\nTOOL_INPUT' },
      { role: 'user', text: '' },
      { role: 'user', text: 'RESUMED_PROMPT' },
    ])
  })

  it('reads a request without history as the current prompt alone', () => {
    expect(kiroModelTurns(request({ currentMessage: { userInputMessage: { content: 'FIRST_PROMPT' } } }))).toEqual([{ role: 'user', text: 'FIRST_PROMPT' }])
  })

  it('keeps an empty assistant turn and a non-string user content as empty text', () => {
    expect(kiroModelTurns(request({
      history: [{ assistantResponseMessage: {} }, { userInputMessage: { content: 0 } }],
      currentMessage: { userInputMessage: { content: '' } },
    }))).toEqual([{ role: 'assistant', text: '' }, { role: 'user', text: '' }, { role: 'user', text: '' }])
  })

  it('requires the native event-stream protocol', () => {
    expect(() => kiroModelTurns({ protocol: 'openai-chat-completions', path: '/chat/completions', body: { messages: [] } })).toThrow('native event-stream request')
  })

  it.each([undefined, null, 'state', []])('rejects the conversation state %j', (state) => {
    expect(() => kiroModelTurns(request(state))).toThrow('no conversation state')
  })

  it('rejects a history that is not an array', () => {
    expect(() => kiroModelTurns(request({ history: {}, currentMessage: { userInputMessage: { content: 'PROMPT' } } }))).toThrow('history is not an array')
  })

  it.each([{}, { currentMessage: {} }, { currentMessage: { userInputMessage: {} } }, { currentMessage: { userInputMessage: { content: null } } }])('rejects an absent current prompt: %j', (state) => {
    expect(() => kiroModelTurns(request(state))).toThrow('no current user message')
  })
})
