import { describe, expect, it } from 'vitest'
import { mcpToolCallDisplayName, parseMcpContentItem } from './mcpToolCall'

describe('mcptoolcalldisplayname', () => {
  it('returns "server / tool" when server is set', () => {
    expect(mcpToolCallDisplayName({ server: 'Tavily', tool: 'tavily_search' }))
      .toBe('Tavily / tavily_search')
  })

  it('returns just the tool when server is empty', () => {
    expect(mcpToolCallDisplayName({ server: '', tool: 'orphan' })).toBe('orphan')
  })
})

describe('parsemcpcontentitem', () => {
  it('parses text blocks', () => {
    expect(parseMcpContentItem({ type: 'text', text: 'hello' }))
      .toEqual({ type: 'text', text: 'hello' })
  })

  it('parses image blocks (mimeType + data)', () => {
    expect(parseMcpContentItem({ type: 'image', mimeType: 'image/png', data: 'base64...' }))
      .toEqual({ type: 'image', source: { mimeType: 'image/png', data: 'base64...' } })
  })

  // The already-normalized `urlOrData` shape, whose value can be a URL or bare
  // base64. The raw MCP `url` key is a different branch, covered in
  // `lib/imageBlocks.test.ts`.
  it('parses image blocks (mimeType + urlOrData holding a URL)', () => {
    expect(parseMcpContentItem({ type: 'image', mimeType: 'image/png', urlOrData: 'https://example.com/x.png' }))
      .toEqual({ type: 'image', source: { mimeType: 'image/png', url: 'https://example.com/x.png' } })
  })

  it('parses the Anthropic nested source shape, which Claude tool_results use', () => {
    expect(parseMcpContentItem({ type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'AAAA' } }))
      .toEqual({ type: 'image', source: { mimeType: 'image/png', data: 'AAAA' } })
  })

  it('keeps an image block that carries no payload, so the row says so', () => {
    expect(parseMcpContentItem({ type: 'image', mimeType: 'image/png' }))
      .toEqual({ type: 'image', source: { mimeType: 'image/png' } })
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
