import { describe, expect, it } from 'vitest'
import { imageBlockHasPayload, imageBlockToMarkdown, parseImageBlock } from './imageBlocks'

describe('parseimageblock', () => {
  it('parses the Anthropic base64 shape (Claude Read, MCP bridge, notebook output)', () => {
    expect(parseImageBlock({ type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'AAAA' } }))
      .toEqual({ data: 'AAAA', mimeType: 'image/png' })
  })

  it('parses the Anthropic url shape', () => {
    expect(parseImageBlock({ type: 'image', source: { type: 'url', url: 'https://example.com/x.png' } }))
      .toEqual({ url: 'https://example.com/x.png' })
  })

  it('parses the flat MCP/ACP/Pi shape', () => {
    expect(parseImageBlock({ type: 'image', data: 'AAAA', mimeType: 'image/jpeg' }))
      .toEqual({ data: 'AAAA', mimeType: 'image/jpeg' })
  })

  it('parses the MCP url variant', () => {
    expect(parseImageBlock({ type: 'image', url: 'https://example.com/x.png', mimeType: 'image/png' }))
      .toEqual({ url: 'https://example.com/x.png', mimeType: 'image/png' })
  })

  it('parses the Codex inputImage shape', () => {
    expect(parseImageBlock({ type: 'inputImage', imageUrl: 'data:image/png;base64,AAAA' }))
      .toEqual({ url: 'data:image/png;base64,AAAA' })
  })

  it('parses the ZCode part shape', () => {
    expect(parseImageBlock({ type: 'image', mediaType: 'image/webp', dataUrl: 'data:image/webp;base64,AAAA' }))
      .toEqual({ url: 'data:image/webp;base64,AAAA', mimeType: 'image/webp' })
  })

  it('parses the already-normalized urlOrData shape, splitting url from base64', () => {
    expect(parseImageBlock({ type: 'image', mimeType: 'image/png', urlOrData: 'AAAA' }))
      .toEqual({ data: 'AAAA', mimeType: 'image/png' })
    expect(parseImageBlock({ type: 'image', mimeType: 'image/png', urlOrData: 'data:image/png;base64,AAAA' }))
      .toEqual({ url: 'data:image/png;base64,AAAA', mimeType: 'image/png' })
    expect(parseImageBlock({ type: 'image', mimeType: 'image/png', urlOrData: 'https://example.com/x.png' }))
      .toEqual({ url: 'https://example.com/x.png', mimeType: 'image/png' })
  })

  it('reads the ACP file:// uri as the image file path', () => {
    expect(parseImageBlock({ type: 'image', data: 'AAAA', mimeType: 'image/png', uri: 'file:///repo/a%20b.png' }))
      .toEqual({ data: 'AAAA', mimeType: 'image/png', filePath: '/repo/a b.png' })
  })

  it('ignores a non-file uri, which names no local file to open', () => {
    expect(parseImageBlock({ type: 'image', data: 'AAAA', mimeType: 'image/png', uri: 'https://example.com/x.png' }))
      .toEqual({ data: 'AAAA', mimeType: 'image/png' })
  })

  it('keeps a payload-less image block so the row can say an image was returned', () => {
    // Anthropic's `source:{type:'file'}` names a file on Anthropic's servers,
    // and an MCP server may state only a MIME type. Dropping either would make
    // the image vanish from the transcript with no trace.
    expect(parseImageBlock({ type: 'image', source: { type: 'file', file_id: 'f1' } })).toEqual({})
    expect(parseImageBlock({ type: 'image', mimeType: 'image/png' })).toEqual({ mimeType: 'image/png' })
  })

  it('keeps base64 with no mime type, which renders as "unsupported format"', () => {
    expect(parseImageBlock({ type: 'image', data: 'AAAA' })).toEqual({ data: 'AAAA', mimeType: undefined })
  })

  it('returns null for a block that is not an image', () => {
    expect(parseImageBlock({ type: 'text', text: 'hi' })).toBeNull()
    expect(parseImageBlock({ type: 'audio', data: 'AAAA' })).toBeNull()
    expect(parseImageBlock({})).toBeNull()
  })

  it('returns null for a non-object', () => {
    expect(parseImageBlock(null as never)).toBeNull()
    expect(parseImageBlock('image' as never)).toBeNull()
  })

  it('returns an empty source for an inputImage with no url', () => {
    expect(parseImageBlock({ type: 'inputImage' })).toEqual({})
  })
})

describe('imageblockhaspayload', () => {
  it('is true only when there is something to render', () => {
    expect(imageBlockHasPayload({ data: 'AAAA' })).toBe(true)
    expect(imageBlockHasPayload({ url: 'data:image/png;base64,AAAA' })).toBe(true)
    expect(imageBlockHasPayload({ mimeType: 'image/png' })).toBe(false)
    expect(imageBlockHasPayload({})).toBe(false)
  })
})

describe('imageblocktomarkdown', () => {
  it('embeds base64 with a mime type', () => {
    expect(imageBlockToMarkdown({ data: 'AAAA', mimeType: 'image/png' }))
      .toBe('![image](data:image/png;base64,AAAA)')
  })

  it('embeds a pre-formed data URL', () => {
    expect(imageBlockToMarkdown({ url: 'data:image/png;base64,AAAA' }))
      .toBe('![image](data:image/png;base64,AAAA)')
  })

  it('links an external URL rather than embedding it, so rendering fetches nothing', () => {
    expect(imageBlockToMarkdown({ url: 'https://example.com/x.png' }))
      .toBe('[image](https://example.com/x.png)')
  })

  it('returns null when there is nothing to point at', () => {
    expect(imageBlockToMarkdown({ mimeType: 'image/png' })).toBeNull()
    // Base64 with no mime type cannot form a data URL at all.
    expect(imageBlockToMarkdown({ data: 'AAAA' })).toBeNull()
  })
})
