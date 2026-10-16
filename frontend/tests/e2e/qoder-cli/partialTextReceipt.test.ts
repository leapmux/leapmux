import { describe, expect, it } from 'vitest'
import { hasQoderPartialTextReceipt } from './partialTextReceipt'

function frame(text: unknown, parent?: unknown) {
  return { type: 'stream_event', parent_tool_use_id: parent, event: { type: 'content_block_delta', delta: { type: 'text_delta', text } } }
}

describe('hasQoderPartialTextReceipt', () => {
  it('requires the complete text across native root chunks', () => {
    expect(hasQoderPartialTextReceipt([frame('part'), frame('ial', null)], 'partial')).toBe(true)
    expect(hasQoderPartialTextReceipt([frame('part')], 'partial')).toBe(false)
  })

  it('excludes child output and invalid frames', () => {
    const frames = [null, false, {}, frame('partial', 'child'), frame('partial', false), frame(42), { type: 'assistant', text: 'partial' }]
    expect(hasQoderPartialTextReceipt(frames, 'partial')).toBe(false)
    expect(hasQoderPartialTextReceipt([{ ...frame('partial'), event: { type: 'message_start' } }], 'partial')).toBe(false)
  })

  it('rejects an empty expected text', () => {
    expect(() => hasQoderPartialTextReceipt([], '')).toThrow('nonempty')
  })
})
