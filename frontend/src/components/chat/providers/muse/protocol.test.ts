import { describe, expect, it } from 'vitest'
import { MUSE_ITEM_KIND, MUSE_METHOD } from '~/generated/contracts/muse-protocol'
import { museItem, museItemText, museParams } from './protocol'

describe('museItem', () => {
  it.each([MUSE_METHOD.ItemStarted, MUSE_METHOD.ItemUpdated, MUSE_METHOD.ItemCompleted])('reads the native item from %s without changing it', (method) => {
    const item = {
      itemId: 'native-item',
      kind: MUSE_ITEM_KIND.AgentMessage,
      futureField: { value: 0 },
    }
    const frame = {
      method,
      params: { sessionId: 'native-session', item },
    }
    const original = structuredClone(frame)
    expect(museItem(frame)).toBe(item)
    expect(frame).toEqual(original)
  })

  it.each([
    undefined,
    null,
    false,
    0,
    '',
    [],
    {},
    { method: MUSE_METHOD.ItemCompleted },
    {
      method: MUSE_METHOD.TurnCompleted,
      params: {
        item: { itemId: 'i', kind: 'agentMessage' },
      },
    },
    {
      method: MUSE_METHOD.ItemCompleted,
      params: { item: null },
    },
    {
      method: MUSE_METHOD.ItemCompleted,
      params: { item: [] },
    },
    {
      method: MUSE_METHOD.ItemCompleted,
      params: {
        item: { itemId: 0, kind: 'agentMessage' },
      },
    },
    {
      method: MUSE_METHOD.ItemCompleted,
      params: {
        item: { itemId: 'i', kind: '' },
      },
    },
  ].map(frame => ({ frame })))('rejects an unreadable item envelope $frame', ({ frame }) => {
    expect(museItem(frame)).toBeUndefined()
  })

  it('preserves a native item with an unknown kind', () => {
    const item = { itemId: 'native-item', kind: 'futureItem', text: 'Native preview' }
    expect(museItem({
      method: MUSE_METHOD.ItemCompleted,
      params: { item },
    })).toBe(item)
  })
})

describe('museParams', () => {
  it('keeps the exact native params object', () => {
    const params = {
      zero: 0,
      empty: '',
      future: { value: false },
    }
    expect(museParams({ method: 'future/event', params })).toBe(params)
  })

  it.each([
    undefined,
    null,
    false,
    0,
    '',
    [],
    {},
    { method: 0, params: {} },
    { method: '', params: {} },
    { method: 'future/event', params: null },
    { method: 'future/event', params: [] },
  ].map(frame => ({ frame })))('rejects unreadable params $frame', ({ frame }) => {
    expect(museParams(frame)).toBeUndefined()
  })
})

describe('museItemText', () => {
  it('keeps native text and its whitespace', () => {
    expect(museItemText({ kind: MUSE_ITEM_KIND.AgentMessage, text: '  Native\ntext  ' })).toBe('  Native\ntext  ')
  })

  it('joins the native reasoning summary in its original order', () => {
    expect(museItemText({
      kind: MUSE_ITEM_KIND.Reasoning,
      summary: ['First', 'Second'],
      text: 'Fallback',
    })).toBe('First\n\nSecond')
  })

  it.each([
    undefined,
    {},
    { text: null },
    { text: 0 },
    { text: false },
  ])('keeps an absent or malformed text empty %j', (item) => {
    expect(museItemText(item)).toBe('')
  })

  it('uses native text when the reasoning summary supplies no text', () => {
    expect(museItemText({
      kind: MUSE_ITEM_KIND.Reasoning,
      summary: [null, 0],
      text: 'Native fallback',
    })).toBe('Native fallback')
  })
})
