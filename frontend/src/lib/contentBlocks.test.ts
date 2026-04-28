import { describe, expect, it } from 'vitest'
import { getMessageContent, joinContentParagraphs, markdownImageFormatter } from './contentBlocks'

describe('getMessageContent', () => {
  it('returns null for non-object inputs', () => {
    expect(getMessageContent(null)).toBeNull()
    expect(getMessageContent(undefined)).toBeNull()
    expect(getMessageContent({})).toBeNull()
  })

  it('returns null when message is missing or not an object', () => {
    expect(getMessageContent({ message: null })).toBeNull()
    expect(getMessageContent({ message: 'string' })).toBeNull()
  })

  it('returns null when message.content is not an array', () => {
    expect(getMessageContent({ message: { content: 'plain' } })).toBeNull()
    expect(getMessageContent({ message: { content: { type: 'text' } } })).toBeNull()
  })

  it('returns the content array', () => {
    const blocks = [{ type: 'text', text: 'hi' }]
    expect(getMessageContent({ message: { content: blocks } })).toBe(blocks)
  })
})

describe('joinContentParagraphs', () => {
  it('returns empty string for null/undefined', () => {
    expect(joinContentParagraphs(null, { text: 'text' })).toBe('')
    expect(joinContentParagraphs(undefined, { text: 'text' })).toBe('')
  })

  it('joins consecutive text blocks with two newlines', () => {
    expect(joinContentParagraphs([
      { type: 'text', text: 'A' },
      { type: 'text', text: 'B' },
    ], { text: 'text' })).toBe('A\n\nB')
  })

  it('preserves interleaved order across kinds', () => {
    expect(joinContentParagraphs([
      { type: 'text', text: 'A' },
      { type: 'thinking', thinking: 'B' },
      { type: 'text', text: 'C' },
    ], { text: 'text', thinking: 'thinking' })).toBe('A\n\nB\n\nC')
  })

  it('pads up to two newlines when a block ends with one', () => {
    expect(joinContentParagraphs([
      { type: 'text', text: 'A\n' },
      { type: 'text', text: 'B' },
    ], { text: 'text' })).toBe('A\n\nB')
  })

  it('adds nothing when a block already ends with two newlines', () => {
    expect(joinContentParagraphs([
      { type: 'text', text: 'A\n\n' },
      { type: 'text', text: 'B' },
    ], { text: 'text' })).toBe('A\n\nB')
  })

  it('preserves three or more trailing newlines (at-least-two semantics)', () => {
    expect(joinContentParagraphs([
      { type: 'text', text: 'A\n\n\n' },
      { type: 'text', text: 'B' },
    ], { text: 'text' })).toBe('A\n\n\nB')
  })

  it('skips empty-string blocks (no leading separator)', () => {
    expect(joinContentParagraphs([
      { type: 'text', text: '' },
      { type: 'text', text: 'A' },
      { type: 'text', text: '' },
      { type: 'text', text: 'B' },
    ], { text: 'text' })).toBe('A\n\nB')
  })

  it('skips block types not in kinds', () => {
    expect(joinContentParagraphs([
      { type: 'text', text: 'A' },
      { type: 'image', data: 'base64' },
      { type: 'text', text: 'B' },
    ], { text: 'text' })).toBe('A\n\nB')
  })

  it('returns empty when no block matches the kinds', () => {
    expect(joinContentParagraphs([
      { type: 'image', data: 'x' },
      { type: 'tool_use', name: 'foo' },
    ], { text: 'text' })).toBe('')
  })

  it('skips non-object entries defensively', () => {
    expect(joinContentParagraphs([
      'string',
      null,
      { type: 'text', text: 'kept' },
    ] as never, { text: 'text' })).toBe('kept')
  })

  it('reads from custom field names per kind', () => {
    expect(joinContentParagraphs([
      { type: 'text', text: 'visible' },
      { type: 'thinking', thinking: 'reasoning' },
    ], { text: 'text', thinking: 'thinking' })).toBe('visible\n\nreasoning')
  })

  it('embeds Pi-shape base64 images as Markdown by default', () => {
    expect(joinContentParagraphs([
      { type: 'text', text: 'see this' },
      { type: 'image', mimeType: 'image/png', data: 'BASE64' },
    ], { text: 'text' })).toBe('see this\n\n![image](data:image/png;base64,BASE64)')
  })

  it('embeds Anthropic-shape base64 images as Markdown', () => {
    expect(joinContentParagraphs([
      { type: 'image', source: { type: 'base64', media_type: 'image/jpeg', data: 'BASE64' } },
    ], { text: 'text' })).toBe('![image](data:image/jpeg;base64,BASE64)')
  })

  it('emits a hyperlink (not inline embed) for external image URLs', () => {
    expect(joinContentParagraphs([
      { type: 'text', text: 'find at' },
      { type: 'image', source: { type: 'url', url: 'https://example.com/x.png' } },
    ], { text: 'text' })).toBe('find at\n\n[image](https://example.com/x.png)')
  })

  it('handles MCP urlOrData shape (data URL → embed, http URL → link)', () => {
    expect(joinContentParagraphs([
      { type: 'image', urlOrData: 'data:image/png;base64,ABC' },
    ], { text: 'text' })).toBe('![image](data:image/png;base64,ABC)')
    expect(joinContentParagraphs([
      { type: 'image', urlOrData: 'https://example.com/x.png' },
    ], { text: 'text' })).toBe('[image](https://example.com/x.png)')
  })

  it('skips images when formatOther is overridden to return null', () => {
    expect(joinContentParagraphs([
      { type: 'text', text: 'a' },
      { type: 'image', mimeType: 'image/png', data: 'BASE64' },
      { type: 'text', text: 'b' },
    ], { text: 'text' }, () => null)).toBe('a\n\nb')
  })
})

describe('markdownImageFormatter', () => {
  it('returns null for non-image blocks', () => {
    expect(markdownImageFormatter({ type: 'text', text: 'x' })).toBeNull()
    expect(markdownImageFormatter({ type: 'thinking', thinking: 'x' })).toBeNull()
  })

  it('returns null for image blocks with no recognizable shape', () => {
    expect(markdownImageFormatter({ type: 'image' })).toBeNull()
    expect(markdownImageFormatter({ type: 'image', source: { type: 'unknown' } })).toBeNull()
  })

  it('formats Pi-shape images as inline Markdown', () => {
    expect(markdownImageFormatter({ type: 'image', mimeType: 'image/png', data: 'XXX' }))
      .toBe('![image](data:image/png;base64,XXX)')
  })

  it('formats Anthropic-shape base64 images as inline Markdown', () => {
    expect(markdownImageFormatter({
      type: 'image',
      source: { type: 'base64', media_type: 'image/jpeg', data: 'YYY' },
    })).toBe('![image](data:image/jpeg;base64,YYY)')
  })

  it('formats Anthropic-shape URL images as a hyperlink', () => {
    expect(markdownImageFormatter({
      type: 'image',
      source: { type: 'url', url: 'https://example.com/a.png' },
    })).toBe('[image](https://example.com/a.png)')
  })
})
