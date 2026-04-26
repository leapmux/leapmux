import { describe, expect, it } from 'vitest'
import { imageRenderInfo, mcpToolCallDisplayName, parseMcpContentItem } from './mcpToolCall'

describe('mcpToolCallDisplayName', () => {
  it('returns "server / tool" when server is set', () => {
    expect(mcpToolCallDisplayName({ server: 'Tavily', tool: 'tavily_search' }))
      .toBe('Tavily / tavily_search')
  })

  it('returns just the tool when server is empty', () => {
    expect(mcpToolCallDisplayName({ server: '', tool: 'orphan' })).toBe('orphan')
  })
})

describe('parseMcpContentItem', () => {
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

describe('imageRenderInfo', () => {
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
