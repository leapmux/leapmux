import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { mcpToolCallDisplayName, parseMcpContentItem, parseMcpToolName } from '../model/mcpToolCall'
import { genericResultCollapsible, GenericToolBody } from './genericToolCall'

it('gives an empty completed result visible content without inventing copyable output', () => {
  const { container } = render(() => <GenericToolBody request={{ args: {} }} result={{ content: [] }} status="completed" />)
  expect(container.textContent).toBe('[no output]')
})

it('preserves failed text blocks with the same formatting as an error field', () => {
  const { container } = render(() => (
    <GenericToolBody request={{ args: {} }} result={{ content: [{ type: 'text' as const, text: 'Access denied\n  Detail' }] }} status="failed" />
  ))
  expect(container.querySelector('p')).toBeNull()
  expect(container.textContent).toBe('Access denied\n  Detail')
})

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
  it('preserves the text of an embedded MCP resource', () => {
    expect(parseMcpContentItem({ type: 'resource', resource: { uri: 'probe://note', mimeType: 'text/plain', text: 'Resource contents' } }))
      .toEqual({ type: 'resource', uri: 'probe://note', mimeType: 'text/plain', text: 'Resource contents' })
  })

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

describe('parseMcpToolName', () => {
  it('splits server and tool', () => {
    expect(parseMcpToolName('mcp__github__create_issue')).toEqual({ server: 'github', tool: 'create_issue' })
  })

  it('preserves further __ segments in the tool name', () => {
    expect(parseMcpToolName('mcp__github__search__repos')).toEqual({ server: 'github', tool: 'search__repos' })
  })

  it('returns null for missing parts', () => {
    expect(parseMcpToolName('mcp__')).toBeNull()
    expect(parseMcpToolName('mcp__github__')).toBeNull()
    expect(parseMcpToolName('mcp____echo')).toBeNull()
    expect(parseMcpToolName('Bash')).toBeNull()
    expect(parseMcpToolName('')).toBeNull()
  })
})

// A malformed row is a renderer defect if it crashes the view. A generic result
// whose `content` array is absent (a provider wrote the execute shape under the
// generic kind) must render and answer, not throw.
describe('genericResultCollapsible', () => {
  it('reads a result with no content array without throwing', () => {
    expect(genericResultCollapsible({ output: 'Saved' } as never, '')).toBe(false)
  })

  it('reads a result with an empty content array without throwing', () => {
    expect(genericResultCollapsible({ content: [] }, '')).toBe(false)
  })
})

describe('GenericToolBody', () => {
  it('renders a result with no content array without throwing', () => {
    // The body reads `content` alone, so a result shaped otherwise draws the
    // empty notice. It must not throw.
    const { container } = render(() => <GenericToolBody request={{ args: {} }} result={{ output: 'Saved' } as never} status="completed" />)
    expect(container.textContent).toBe('[no output]')
  })
})
