import { describe, expect, it } from 'vitest'
import { deepseekHarnessAssistantBlock, deepseekHarnessCallId, deepseekHarnessContentText, deepseekHarnessEventData } from './protocol'

describe('deepseekHarnessAssistantBlock', () => {
  const message = { type: 'assistant/message', data: { message: { content: [{ type: 'reasoning', text: 'Reason' }, { type: 'text', text: 'Answer' }] } } }

  it('selects zero and later native blocks without changing the event', () => {
    expect(deepseekHarnessAssistantBlock({ ...message, blockIndex: 0 })).toEqual({ type: 'reasoning', text: 'Reason' })
    expect(deepseekHarnessAssistantBlock({ ...message, blockIndex: 1 })).toEqual({ type: 'text', text: 'Answer' })
    expect(message).not.toHaveProperty('blockIndex')
  })

  it.each([{ value: undefined }, { value: null }, { value: -1 }, { value: 0.5 }, { value: '0' }, { value: Number.NaN }, { value: Number.POSITIVE_INFINITY }, { value: 1000000 }])('rejects an invalid block identity: $value', ({ value }) => {
    expect(deepseekHarnessAssistantBlock({ ...message, blockIndex: value })).toBeUndefined()
  })

  it('rejects a malformed content block and another event type', () => {
    expect(deepseekHarnessAssistantBlock({ type: 'assistant/message', blockIndex: 0, data: { message: { content: [null] } } })).toBeUndefined()
    expect(deepseekHarnessAssistantBlock({ ...message, type: 'user/message', blockIndex: 0 })).toBeUndefined()
  })
})

describe('deepseekHarnessContentText', () => {
  it('preserves the native sequence and whitespace while excluding images', () => {
    expect(deepseekHarnessContentText([{ type: 'text', text: 'a\n' }, { type: 'image', data: 'secret-base64' }, { type: 'text', text: '' }, { type: 'text', text: ' b' }])).toBe('a\n b')
    expect(deepseekHarnessContentText(null)).toBe('')
    expect(deepseekHarnessContentText([{ type: 'text', text: 0 }, null])).toBe('')
  })
})

describe('deepseekHarnessEventData', () => {
  it('requires the requested event and an object payload', () => {
    expect(deepseekHarnessEventData({ type: 'turn/start', data: {} }, 'turn/start')).toEqual({})
    expect(deepseekHarnessEventData({ type: 'turn/end', data: {} }, 'turn/start')).toBeUndefined()
    for (const data of [null, [], '', 0])
      expect(deepseekHarnessEventData({ type: 'turn/start', data })).toBeUndefined()
  })
})

describe('deepseekHarnessCallId', () => {
  it('reads the distinct call and result shapes', () => {
    expect(deepseekHarnessCallId({ type: 'tool/call', data: { callId: 'call' } })).toBe('call')
    expect(deepseekHarnessCallId({ type: 'tool/result', data: { message: { toolCallId: 'call' } } })).toBe('call')
    expect(deepseekHarnessCallId({ type: 'tool/result', data: { callId: 'wrong-shape' } })).toBe('')
    expect(deepseekHarnessCallId(null)).toBe('')
  })
})
