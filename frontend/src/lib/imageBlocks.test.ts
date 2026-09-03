import { describe, expect, it } from 'vitest'
import { imageBlockToMarkdown, MAX_INLINE_IMAGE_BASE64_LEN, parseImageBlock } from './imageBlocks'

describe('parseImageBlock', () => {
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

  it('ignores a non-file uri, which specifies no local file to open', () => {
    expect(parseImageBlock({ type: 'image', data: 'AAAA', mimeType: 'image/png', uri: 'https://example.com/x.png' }))
      .toEqual({ data: 'AAAA', mimeType: 'image/png' })
  })

  it('keeps a payload-less image block so the row can say an image was returned', () => {
    // Anthropic's `source:{type:'file'}` states a file on Anthropic's servers,
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

  // An empty `data` is no payload, not an empty payload: the branches below it must
  // still get their turn, and the source must end up renderable-as-nothing
  // rather than as a zero-length base64 the renderer would build a data URL out
  // of.
  it('treats an empty data string as no payload', () => {
    expect(parseImageBlock({ type: 'image', data: '', mimeType: 'image/png' }))
      .toEqual({ mimeType: 'image/png' })
  })

  // A server that sends a number where the spec says string states nothing this
  // parser can use. Dropping the field beats coercing it: `String(123)` would
  // become a MIME type the allowlist then has to reject by accident.
  it('ignores non-string payload and mime fields', () => {
    expect(parseImageBlock({ type: 'image', data: 123, mimeType: 456 })).toEqual({})
    expect(parseImageBlock({ type: 'image', url: null, mimeType: 'image/png' }))
      .toEqual({ mimeType: 'image/png' })
  })

  it('returns an empty source for an inputImage with no url', () => {
    expect(parseImageBlock({ type: 'inputImage' })).toEqual({})
  })
})

describe('imageBlockToMarkdown', () => {
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

describe('imageBlockToMarkdown size cap', () => {
  // The markdown formatter is the DEFAULT block formatter, so it reaches every
  // text destination -- a quoted message, a subagent report card, a scroll-rail
  // preview. The wire allows a message far larger than the inline cap, so
  // without this an oversized screenshot became megabytes of base64 inside an
  // `src` attribute the reader never asked for.
  it('refuses an oversized base64 payload with a labelled placeholder', () => {
    const oversized = 'A'.repeat(MAX_INLINE_IMAGE_BASE64_LEN + 1)
    expect(imageBlockToMarkdown({ data: oversized, mimeType: 'image/png' }))
      .toBe('[image: image/png — too large to embed]')
  })

  it('refuses an oversized already-formed data URL', () => {
    const url = `data:image/png;base64,${'A'.repeat(MAX_INLINE_IMAGE_BASE64_LEN + 1)}`
    expect(imageBlockToMarkdown({ url })).toBe('[image: too large to embed]')
  })

  // The cap measures the PAYLOAD, not the whole string, so a source just under
  // it still embeds rather than tripping on the `data:<mime>;base64,` preamble.
  it('still embeds a payload at the cap', () => {
    const atCap = 'A'.repeat(MAX_INLINE_IMAGE_BASE64_LEN)
    expect(imageBlockToMarkdown({ data: atCap, mimeType: 'image/png' }))
      .toBe(`![image](data:image/png;base64,${atCap})`)
  })
})

describe('parseImageBlock data-URL tolerance', () => {
  // A server that puts a complete data: URL under `data` is off-spec but real,
  // and the reader this parser replaced sniffed for it. Without the sniff the
  // renderer builds `data:<mime>;base64,data:<mime>;base64,...` -- a broken
  // image with no placeholder, and a tab whose decode throws.
  it('routes a data: URL in the `data` key to `url`, not `data`', () => {
    expect(parseImageBlock({ type: 'image', mimeType: 'image/png', data: 'data:image/png;base64,AAAA' }))
      .toEqual({ url: 'data:image/png;base64,AAAA', mimeType: 'image/png' })
  })

  // `:` is not in the base64 alphabet, so the sniff can never misread a real
  // payload as a URL.
  it('leaves ordinary base64 in `data`', () => {
    expect(parseImageBlock({ type: 'image', mimeType: 'image/png', data: 'AAAA' }))
      .toEqual({ data: 'AAAA', mimeType: 'image/png' })
  })
})

describe('imageBlockFilePath platform shapes', () => {
  // Windows workers are supported, and `new URL(...).pathname` keeps the slash
  // before the drive letter. The worker cannot resolve `/C:/...`, so the FILE
  // tab this feeds opened nothing while the IMAGE tab that would have worked
  // was skipped.
  it('strips the leading slash from a Windows drive-letter path', () => {
    expect(parseImageBlock({ type: 'image', data: 'AAAA', mimeType: 'image/png', uri: 'file:///C:/Users/alice/shot.png' }))
      .toMatchObject({ filePath: 'C:/Users/alice/shot.png' })
  })

  // A UNC uri puts the host outside the pathname, so reading the pathname alone
  // silently dropped the server the file lives on.
  it('keeps the host of a UNC uri', () => {
    expect(parseImageBlock({ type: 'image', data: 'AAAA', mimeType: 'image/png', uri: 'file://server/share/a.png' }))
      .toMatchObject({ filePath: '//server/share/a.png' })
  })

  it('leaves a POSIX path alone, percent-decoding it', () => {
    expect(parseImageBlock({ type: 'image', data: 'AAAA', mimeType: 'image/png', uri: 'file:///home/alice/a%20b.png' }))
      .toMatchObject({ filePath: '/home/alice/a b.png' })
  })
})

describe('imageBlockToMarkdown MIME allowlist', () => {
  // The allowlist lived in the transcript row's component, which a lib module
  // cannot import -- so this path hand-copied the size cap and had no allowlist
  // at all. Both now come from `imageRenderInfo`, so the two destinations agree
  // on which types embed.
  it('refuses a type the transcript row refuses, and says which type', () => {
    expect(imageBlockToMarkdown({ data: 'AAAA', mimeType: 'application/pdf' }))
      .toBe('[image: application/pdf — unsupported format]')
  })

  it('refuses that type given as a data URL', () => {
    expect(imageBlockToMarkdown({ url: 'data:application/pdf;base64,AAAA' })).toBeNull()
  })

  // SVG IS allowlisted: every consumer mounts through `<img>`, which renders it
  // in secure static mode, and the file viewer already drew on-disk SVGs the
  // same way. Refusing it here served no guarantee the element does not already
  // give.
  it('embeds an SVG, the same way the file viewer renders one off disk', () => {
    expect(imageBlockToMarkdown({ data: 'AAAA', mimeType: 'image/svg+xml' }))
      .toBe('![image](data:image/svg+xml;base64,AAAA)')
  })

  it('still embeds a raster type', () => {
    expect(imageBlockToMarkdown({ data: 'AAAA', mimeType: 'image/png' }))
      .toBe('![image](data:image/png;base64,AAAA)')
  })
})
