import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { cursorModelTurns } from './modelTurns'

describe('cursorModelTurns', () => {
  it('reads the service history in order, then the current prompt', () => {
    const request: MockModelRequestRecord = {
      protocol: 'openai-responses',
      path: '/agent.v1.AgentService/Run',
      body: { prompt: 'RESUMED_PROMPT', attachments: [], conversationId: 'native-conversation' },
      serverContext: { conversationId: 'native-conversation', messages: [{ role: 'user', content: 'ORIGINAL_PROMPT' }, { role: 'assistant', content: 'ORIGINAL_ANSWER' }] },
    }
    expect(cursorModelTurns(request)).toEqual([
      { role: 'user', text: 'ORIGINAL_PROMPT' },
      { role: 'assistant', text: 'ORIGINAL_ANSWER' },
      { role: 'user', text: 'RESUMED_PROMPT' },
    ])
  })

  it('reads a request without service history as the current prompt alone', () => {
    expect(cursorModelTurns({ protocol: 'openai-responses', path: '/run', body: { prompt: '' } })).toEqual([{ role: 'user', text: '' }])
    expect(cursorModelTurns({ protocol: 'openai-responses', path: '/run', body: { prompt: 'FIRST_PROMPT' }, serverContext: { conversationId: 'c', messages: [] } }))
      .toEqual([{ role: 'user', text: 'FIRST_PROMPT' }])
  })

  it.each([null, {}, { prompt: 0 }, { prompt: null }, []])('rejects a Run body without a prompt: %j', (body) => {
    expect(() => cursorModelTurns({ protocol: 'openai-responses', path: '/run', body })).toThrow('prompt of its native Run request')
  })
})
