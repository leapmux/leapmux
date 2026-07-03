import { describe, expect, it } from 'vitest'
import { imageRenderInfo, imageReservationMatchesDecoded, imageReservationStyle, mcpToolCallDisplayName, parseMcpContentItem } from './mcpToolCall'

describe('mcptoolcalldisplayname', () => {
  it('returns "server / tool" when server is set', () => {
    expect(mcpToolCallDisplayName({ server: 'Tavily', tool: 'tavily_search' }))
      .toBe('Tavily / tavily_search')
  })

  it('returns just the tool when server is empty', () => {
    expect(mcpToolCallDisplayName({ server: '', tool: 'orphan' })).toBe('orphan')
  })
})

describe('imagereservationstyle', () => {
  // The width formula must reproduce exactly what auto layout yields after
  // the image decodes: height = min(h, h/w * containerWidth, MAX_HEIGHT).
  it('clamps by natural width for images that fit', () => {
    expect(imageReservationStyle({ width: 100, height: 50 })).toEqual({
      'aspect-ratio': '100 / 50',
      'width': 'min(100px, 100%, 640.00px)', // natural 100px wins
    })
  })

  it('clamps by width-at-max-height for tall images', () => {
    // 800x1600 hits the 320px max height at width 160 — the reserved box
    // stops at the visible image edge instead of spanning the container.
    expect(imageReservationStyle({ width: 800, height: 1600 })).toEqual({
      'aspect-ratio': '800 / 1600',
      'width': 'min(800px, 100%, 160.00px)',
    })
  })

  it('leaves wide images to the container clamp', () => {
    expect(imageReservationStyle({ width: 640, height: 480 })).toEqual({
      'aspect-ratio': '640 / 480',
      'width': 'min(640px, 100%, 426.67px)',
    })
  })
})

describe('imagereservationmatchesdecoded', () => {
  it('rejects decoded dimensions that preserve ratio but not absolute size', () => {
    expect(imageReservationMatchesDecoded(
      { width: 100, height: 50 },
      { naturalWidth: 200, naturalHeight: 100 },
    )).toBe(false)
  })

  it('accepts exactly matching decoded dimensions', () => {
    expect(imageReservationMatchesDecoded(
      { width: 100, height: 50 },
      { naturalWidth: 100, naturalHeight: 50 },
    )).toBe(true)
  })
})

describe('parsemcpcontentitem', () => {
  it('parses text blocks', () => {
    expect(parseMcpContentItem({ type: 'text', text: 'hello' }))
      .toEqual({ type: 'text', text: 'hello' })
  })

  it('parses image blocks (mimeType + data)', () => {
    expect(parseMcpContentItem({ type: 'image', mimeType: 'image/png', data: 'base64...' }))
      .toEqual({ type: 'image', mimeType: 'image/png', urlOrData: 'base64...' })
  })

  it('parses image blocks (mimeType + url)', () => {
    expect(parseMcpContentItem({ type: 'image', mimeType: 'image/png', url: 'https://example.com/x.png' }))
      .toEqual({ type: 'image', mimeType: 'image/png', urlOrData: 'https://example.com/x.png' })
  })

  it('parses resource blocks', () => {
    expect(parseMcpContentItem({ type: 'resource', uri: 'file:///x', mimeType: 'text/plain' }))
      .toEqual({ type: 'resource', uri: 'file:///x', mimeType: 'text/plain' })
  })

  it('classifies unknown shapes as `unknown`', () => {
    expect(parseMcpContentItem({ type: 'audio', data: 'x' }))
      .toEqual({ type: 'unknown', raw: { type: 'audio', data: 'x' } })
  })

  it('classifies primitives as `unknown`', () => {
    expect(parseMcpContentItem('plain string')).toEqual({ type: 'unknown', raw: 'plain string' })
    expect(parseMcpContentItem(null)).toEqual({ type: 'unknown', raw: null })
  })

  it('classifies text blocks without a string text field as `unknown`', () => {
    expect(parseMcpContentItem({ type: 'text' })).toEqual({ type: 'unknown', raw: { type: 'text' } })
  })

  it('classifies resource blocks without a uri as `unknown`', () => {
    expect(parseMcpContentItem({ type: 'resource' })).toEqual({ type: 'unknown', raw: { type: 'resource' } })
  })
})

describe('imagerenderinfo', () => {
  it('returns no-data when urlOrData is missing', () => {
    expect(imageRenderInfo({ type: 'image' })).toEqual({ reason: 'no-data' })
  })

  it('builds an inline data: URL from base64 + allowlisted mime', () => {
    const result = imageRenderInfo({ type: 'image', mimeType: 'image/png', urlOrData: 'AAAA' })
    expect(result.via).toBe('inline')
    expect(result.src).toBe('data:image/png;base64,AAAA')
  })

  it('uppercase mime types are normalized', () => {
    const result = imageRenderInfo({ type: 'image', mimeType: 'IMAGE/PNG', urlOrData: 'AAAA' })
    expect(result.via).toBe('inline')
    expect(result.src).toBe('data:image/png;base64,AAAA')
  })

  it('refuses non-allowlisted mime types (e.g. svg)', () => {
    expect(imageRenderInfo({ type: 'image', mimeType: 'image/svg+xml', urlOrData: 'AAAA' }))
      .toEqual({ reason: 'unsupported-mime' })
  })

  it('refuses base64 without an explicit mime type', () => {
    expect(imageRenderInfo({ type: 'image', urlOrData: 'AAAA' }))
      .toEqual({ reason: 'unsupported-mime' })
  })

  it('refuses base64 over the size cap', () => {
    const huge = 'A'.repeat(7 * 1024 * 1024 + 1)
    expect(imageRenderInfo({ type: 'image', mimeType: 'image/png', urlOrData: huge }))
      .toEqual({ reason: 'too-large' })
  })

  it('caps pre-formed data: URLs by payload size, not metadata prefix length', () => {
    const payload = 'A'.repeat(7 * 1024 * 1024)
    const data = `data:image/png;base64,${payload}`
    expect(imageRenderInfo({ type: 'image', urlOrData: data })).toEqual({
      src: data,
      via: 'inline',
    })
  })

  it('rejects pre-formed data: URLs whose payload exceeds the cap', () => {
    // The over-cap side of the payload-size boundary (one byte past MAX): the
    // `data.length - comma - 1 > MAX` true branch, not otherwise exercised.
    const payload = 'A'.repeat(7 * 1024 * 1024 + 1)
    const data = `data:image/png;base64,${payload}`
    expect(imageRenderInfo({ type: 'image', urlOrData: data }))
      .toEqual({ reason: 'too-large' })
  })

  it('passes through pre-formed data: URLs with allowlisted mime', () => {
    const data = 'data:image/jpeg;base64,XYZ='
    expect(imageRenderInfo({ type: 'image', urlOrData: data })).toEqual({
      src: data,
      via: 'inline',
    })
  })

  it('refuses pre-formed data: URLs with non-allowlisted mime', () => {
    expect(imageRenderInfo({ type: 'image', urlOrData: 'data:image/svg+xml;base64,XYZ=' }))
      .toEqual({ reason: 'unsupported-mime' })
  })

  it('flags http URLs as external (rendered as a link, not inlined)', () => {
    expect(imageRenderInfo({ type: 'image', urlOrData: 'http://example.com/x.png' }))
      .toEqual({ reason: 'external-url' })
  })

  it('flags https URLs as external (rendered as a link, not inlined)', () => {
    expect(imageRenderInfo({ type: 'image', urlOrData: 'https://example.com/x.png' }))
      .toEqual({ reason: 'external-url' })
  })
})
