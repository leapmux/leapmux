import { describe, expect, it } from 'vitest'
import { getMessageContent, joinContentParagraphs, markdownImageFormatter, splitToolResultContent } from './contentBlocks'

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

describe('splitToolResultContent', () => {
  it('returns the same text as joinContentParagraphs with images skipped', () => {
    const blocks = [
      { type: 'text', text: 'before' },
      { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'AAAA' } },
      { type: 'text', text: 'after' },
    ]
    expect(splitToolResultContent(blocks, { text: 'text' }).text)
      .toBe(joinContentParagraphs(blocks, { text: 'text' }, () => null))
  })

  it('keeps the base64 out of the text and hands it back as a source', () => {
    const { text, images } = splitToolResultContent([
      { type: 'text', text: 'here is the screenshot' },
      { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'AAAA' } },
    ], { text: 'text' })
    expect(text).toBe('here is the screenshot')
    expect(text).not.toContain('base64')
    expect(images).toEqual([{ data: 'AAAA', mimeType: 'image/png' }])
  })

  it('preserves wire order across several images', () => {
    // The order IS the contract: an image tab resolves "image N of this
    // message" by re-running this walk on the re-fetched message.
    const { images } = splitToolResultContent([
      { type: 'image', data: 'first', mimeType: 'image/png' },
      { type: 'text', text: 'between' },
      { type: 'image', data: 'second', mimeType: 'image/png' },
      { type: 'thinking', thinking: 'ignored' },
      { type: 'image', data: 'third', mimeType: 'image/png' },
    ], { text: 'text' })
    expect(images.map(i => i.data)).toEqual(['first', 'second', 'third'])
  })

  it('collects a payload-less image so the row still reports it', () => {
    const { text, images } = splitToolResultContent([
      { type: 'image', mimeType: 'image/png' },
    ], { text: 'text' })
    expect(text).toBe('')
    expect(images).toEqual([{ mimeType: 'image/png' }])
  })

  it('returns empty halves for null content', () => {
    expect(splitToolResultContent(null, { text: 'text' })).toEqual({ text: '', images: [] })
  })

  it('leaves non-image, non-text blocks out of both halves', () => {
    const { text, images } = splitToolResultContent([
      { type: 'tool_use', name: 'Bash', input: {} },
    ], { text: 'text' })
    expect(text).toBe('')
    expect(images).toEqual([])
  })
})

describe('forEachContentBlock guarantees', () => {
  // `kinds[block.type]` reached Object.prototype: a block whose `type` is
  // `constructor` or `toString` answered a truthy value, took the text path,
  // read a non-string field and was skipped -- so `formatOther` never saw it.
  // `type` is agent-supplied wire JSON, so any provider can send one.
  it('hands a block whose type is an Object.prototype key to formatOther', () => {
    const seen: string[] = []
    joinContentParagraphs(
      [
        { type: 'constructor', text: 'x' },
        { type: 'toString', text: 'y' },
        { type: 'valueOf' },
        { type: '__proto__' },
      ],
      { text: 'text' },
      (block) => {
        seen.push(String(block.type))
        return null
      },
    )
    expect(seen).toEqual(['constructor', 'toString', 'valueOf', '__proto__'])
  })

  // The ordering guarantee an image tab's permanent `imageIndex` rests on.
  it('visits every non-kinds block exactly once, in wire order', () => {
    const seen: unknown[] = []
    joinContentParagraphs(
      [
        { type: 'text', text: 'a' },
        { type: 'image', id: 1 },
        { type: 'text', text: 'b' },
        { type: 'image', id: 2 },
      ],
      { text: 'text' },
      (block) => {
        seen.push(block.id)
        return null
      },
    )
    expect(seen).toEqual([1, 2])
  })

  // The same walk feeds both sinks, so the index a tab stores and the text a
  // quote shows come from one traversal rather than two that must agree.
  it('splitToolResultContent numbers images by that same order', () => {
    const { text, images } = splitToolResultContent(
      [
        { type: 'image', mimeType: 'image/png', data: 'FIRST' },
        { type: 'text', text: 'between' },
        { type: 'constructor', mimeType: 'image/png', data: 'SECOND' },
        { type: 'image', mimeType: 'image/png', data: 'THIRD' },
      ],
      { text: 'text' },
    )
    expect(text).toBe('between')
    expect(images.map(i => i.data)).toEqual(['FIRST', 'THIRD'])
  })
})
