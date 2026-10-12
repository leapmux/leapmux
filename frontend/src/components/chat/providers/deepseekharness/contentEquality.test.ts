import { describe, expect, it } from 'vitest'
import { deepseekHarnessContentEqual } from './contentEquality'

function content(): ({ type: 'text', text: string } | { type: 'image', attachment: { attachmentId: string, mediaType: string, bytes: number, width: number, height: number } })[] {
  const image = { type: 'image' as const, attachment: { attachmentId: 'native-image', mediaType: 'image/png', bytes: 168, width: 64, height: 64 } }
  return [{ type: 'text', text: '' }, image, { type: 'text', text: 'Native middle.' }, image, { type: 'text', text: 'Native end.' }]
}

describe('deepseekHarnessContentEqual', () => {
  it('compares independently decoded native blocks and preserves repeated image occurrences', () => {
    const original = content()
    const before = structuredClone(original)
    expect(deepseekHarnessContentEqual(original, structuredClone(original))).toBe(true)
    expect(original).toEqual(before)
    expect(deepseekHarnessContentEqual(original, original.slice(0, -1))).toBe(false)
    expect(deepseekHarnessContentEqual(original, [...original].reverse())).toBe(false)
  })

  it.each([
    { field: 'attachmentId', value: 'foreign-image' },
    { field: 'mediaType', value: 'image/jpeg' },
    { field: 'bytes', value: 169 },
    { field: 'width', value: 65 },
    { field: 'height', value: 65 },
  ])('rejects a changed nested native reference field: $field', ({ field, value }) => {
    const original = content()
    const changed = original.map(block => block.type === 'image' ? { ...block, attachment: { ...block.attachment, [field]: value } } : block)
    expect(deepseekHarnessContentEqual(original, changed)).toBe(false)
  })

  it('rejects a missing reference field or an extra block field', () => {
    const original = content()
    const missing = original.map(block => block.type === 'image' ? { ...block, attachment: { attachmentId: 'native-image', mediaType: 'image/png', bytes: 168, width: 64 } } : block)
    expect(deepseekHarnessContentEqual(original, missing)).toBe(false)
    expect(deepseekHarnessContentEqual(original, original.map(block => ({ ...block, extra: true })))).toBe(false)
  })

  it.each([null, undefined, {}, [null], [{ type: 'text' }], [{ type: 'image' }], [{ type: 'other', text: 'Unknown native block.' }]].map(value => ({ value })))('rejects invalid native content: $value', ({ value }) => {
    expect(deepseekHarnessContentEqual(value, value)).toBe(false)
  })

  it('keeps empty content and explicit empty text without accepting different text', () => {
    expect(deepseekHarnessContentEqual([], [])).toBe(true)
    expect(deepseekHarnessContentEqual([{ type: 'text', text: '' }], [{ type: 'text', text: '' }])).toBe(true)
    expect(deepseekHarnessContentEqual([{ type: 'text', text: '' }], [{ type: 'text', text: '0' }])).toBe(false)
    expect(deepseekHarnessContentEqual(Array.from({ length: 1 }), Array.from({ length: 1 }))).toBe(false)
  })
})
