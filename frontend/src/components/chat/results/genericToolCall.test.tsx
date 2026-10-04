import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { mcpToolCallDisplayName, parseMcpContentItem, parseMcpToolName } from '../model/mcpToolCall'
import { genericResultCollapsible, genericResultCopyable, GenericToolBody } from './genericToolCall'

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

  // urlOrData can hold a URL or base64 image data.
  // The raw MCP url field has separate image-reader coverage.
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

// A malformed generic result can omit its content array.
// The renderer must retain a visible response and must not throw.
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
    // A result without content draws the empty notice.
    // It must not throw.
    const { container } = render(() => <GenericToolBody request={{ args: {} }} result={{ output: 'Saved' } as never} status="completed" />)
    expect(container.textContent).toBe('[no output]')
  })
})

describe('GenericToolBody output ownership', () => {
  const metadataJson = '{"calls":[{"args":"native-output-marker"}],"count":0}'

  it('puts execution metadata before actual output and preserves original Copy order', () => {
    const result = { content: [{ type: 'text' as const, text: 'native-output-marker' }], structuredJson: metadataJson, structuredJsonRole: 'metadata' as const }
    const { container } = render(() => <GenericToolBody request={{ args: { code: 'native-output-marker' } }} result={result} status="completed" expanded={() => true} />)
    const outputs = container.querySelectorAll('[data-tool-output-preview]')
    expect(outputs).toHaveLength(1)
    expect(outputs[0]?.textContent).toBe('native-output-marker')
    const text = container.textContent ?? ''
    expect(text.indexOf(metadataJson)).toBeLessThan(text.lastIndexOf('native-output-marker'))
    expect(genericResultCopyable(result)).toBe(`native-output-marker\n\n${metadataJson}`)
  })

  it('cannot certify arguments or explicit structured metadata when output is absent', () => {
    const result = { content: [], structuredJson: metadataJson, structuredJsonRole: 'metadata' as const }
    const { container } = render(() => <GenericToolBody request={{ args: { text: 'native-output-marker' } }} result={result} status="completed" expanded={() => true} />)
    expect(container.textContent).toContain('native-output-marker')
    expect(container.querySelector('[data-tool-output-preview]')).toBeNull()
    expect(genericResultCopyable(result)).toBe(metadataJson)
  })

  it.each(['0', 'false', 'null', '""', '{"nullable":null,"count":0,"enabled":false}'])('owns a genuine structured-only result %s', (structuredJson) => {
    const result = { content: [], structuredJson }
    const { container } = render(() => <GenericToolBody request={{ args: { text: structuredJson } }} result={result} status="completed" expanded={() => true} />)
    const outputs = container.querySelectorAll('[data-tool-output-preview]')
    expect(outputs).toHaveLength(1)
    expect(outputs[0]?.textContent).toBe(structuredJson)
    expect(genericResultCopyable(result)).toBe(structuredJson)
  })

  it('owns actual resource text and error output without owning the resource URI or headings', () => {
    const { container } = render(() => <GenericToolBody request={{ args: {} }} result={{ content: [{ type: 'resource', uri: 'probe://native-output-marker', text: 'returned resource' }], error: 'returned error' }} status="failed" />)
    expect([...container.querySelectorAll('[data-tool-output-preview]')].map(element => element.textContent)).toEqual(['returned resource', 'returned error'])
    expect(container.textContent).toContain('probe://native-output-marker')
  })

  it('does not invent output ownership for an empty completed result', () => {
    const { container } = render(() => <GenericToolBody request={{ args: { text: 'argument-only-marker' } }} result={{ content: [] }} status="completed" />)
    expect(container.textContent).toContain('[no output]')
    expect(container.querySelector('[data-tool-output-preview]')).toBeNull()
  })
})
