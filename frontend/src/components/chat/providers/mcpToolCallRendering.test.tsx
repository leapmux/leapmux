import type { MessageCategory } from '../messageClassification'
import type { RenderContext } from '../messageRenderers'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import './claude'
import './codex'
import './testMocks'

vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: async (_lang: string, code: string) => code.split('\n').map(() => []),
}))

vi.mock('~/lib/tokenCache', () => ({
  getCachedTokens: () => null,
}))

const { renderMessageContent } = await import('../messageRenderers')

function renderClaudeToolResult(parsed: Record<string, unknown>, context?: RenderContext) {
  const category: MessageCategory = { kind: 'tool_result' }
  const result = renderMessageContent(parsed, context, category, AgentProvider.CLAUDE_CODE)
  return render(() => result)
}

function makeMcpToolResult(content: unknown, isError = false) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [{ type: 'tool_result', tool_use_id: 'r1', content, is_error: isError }],
    },
  }
}

function renderCodexItem(item: Record<string, unknown>, context?: RenderContext) {
  const parsed = { item, threadId: 't1', turnId: 'r1' }
  const toolName = String(item.type ?? 'codex')
  const category: MessageCategory = {
    kind: 'tool_use',
    toolName,
    toolUse: parsed,
    content: [],
  }
  const result = renderMessageContent(parsed, context, category, AgentProvider.CODEX)
  return render(() => result)
}

// ---------------------------------------------------------------------------
// Claude MCP tool_result rendering
// ---------------------------------------------------------------------------

describe('claude MCP tool_result rendering', () => {
  it('renders text content blocks via the shared MCP body', () => {
    const parsed = makeMcpToolResult([
      { type: 'text', text: '## Search Result\n\nFound 3 matches.' },
    ])
    const { container } = renderClaudeToolResult(parsed, {
      spanType: 'mcp__claude_ai_Tavily__tavily_search',
    })
    const text = container.textContent ?? ''
    expect(text).toContain('Search Result')
    expect(text).toContain('Found 3 matches.')
  })

  it('renders the error string when is_error is true', () => {
    const parsed = makeMcpToolResult([
      { type: 'text', text: 'Authentication failed' },
    ], true)
    const { container } = renderClaudeToolResult(parsed, {
      spanType: 'mcp__github__create_issue',
    })
    const text = container.textContent ?? ''
    expect(text).toContain('Authentication failed')
  })

  it('falls back to plain text content when result is a string', () => {
    const parsed = makeMcpToolResult('Plain string result')
    const { container } = renderClaudeToolResult(parsed, {
      spanType: 'mcp__github__list_issues',
    })
    expect(container.textContent ?? '').toContain('Plain string result')
  })

  it('renders inline images from base64 data', () => {
    const parsed = makeMcpToolResult([
      { type: 'image', mimeType: 'image/png', data: 'iVBORw0KGgo=' },
    ])
    const { container } = renderClaudeToolResult(parsed, {
      spanType: 'mcp__playwright__screenshot',
    })
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    expect(img?.getAttribute('src')).toBe('data:image/png;base64,iVBORw0KGgo=')
    expect(img?.getAttribute('referrerpolicy')).toBe('no-referrer')
    expect(img?.getAttribute('loading')).toBe('lazy')
  })

  it('keeps renderable inline images in premeasure mode', () => {
    const parsed = makeMcpToolResult([
      { type: 'image', mimeType: 'image/png', data: 'iVBORw0KGgo=' },
    ])
    const { container } = renderClaudeToolResult(parsed, {
      spanType: 'mcp__playwright__screenshot',
      premeasureMode: true,
    })
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    expect(img?.getAttribute('src')).toBe('data:image/png;base64,iVBORw0KGgo=')
    expect(img?.getAttribute('loading')).toBe('eager')
  })

  /** Minimal PNG header (signature + IHDR) declaring the given dimensions. */
  function pngBase64(width: number, height: number): string {
    const u32 = (v: number) => [(v >>> 24) & 0xFF, (v >>> 16) & 0xFF, (v >>> 8) & 0xFF, v & 0xFF]
    const bytes = [
      0x89,
      0x50,
      0x4E,
      0x47,
      0x0D,
      0x0A,
      0x1A,
      0x0A,
      ...u32(13),
      0x49,
      0x48,
      0x44,
      0x52,
      ...u32(width),
      ...u32(height),
    ]
    return btoa(String.fromCharCode(...bytes))
  }

  // NOTE: jsdom's CSS engine drops `min()` widths and `aspect-ratio`
  // declarations, so these tests assert the reservation lifecycle via the
  // data attribute; the exact style formula is unit-tested in
  // results/mcpToolCall.test.tsx (imageReservationStyle).
  it('reserves the intrinsic box for images with a sniffable header', () => {
    const parsed = makeMcpToolResult([
      { type: 'image', mimeType: 'image/png', data: pngBase64(640, 480) },
    ])
    const { container } = renderClaudeToolResult(parsed, {
      spanType: 'mcp__playwright__screenshot',
    })
    const img = container.querySelector('img')!
    expect(img.getAttribute('data-size-reserved')).toBe('1')
  })

  it('leaves unsniffable images unreserved (truncated header)', () => {
    const parsed = makeMcpToolResult([
      { type: 'image', mimeType: 'image/png', data: 'iVBORw0KGgo=' },
    ])
    const { container } = renderClaudeToolResult(parsed, {
      spanType: 'mcp__playwright__screenshot',
    })
    const img = container.querySelector('img')!
    expect(img.getAttribute('data-size-reserved')).toBeNull()
    expect(img.getAttribute('style')).toBeNull()
  })

  it('drops the reservation when the decoded image disagrees with the sniffed ratio', () => {
    const parsed = makeMcpToolResult([
      { type: 'image', mimeType: 'image/png', data: pngBase64(640, 480) },
    ])
    const { container } = renderClaudeToolResult(parsed, {
      spanType: 'mcp__playwright__screenshot',
    })
    const img = container.querySelector('img')!
    expect(img.getAttribute('data-size-reserved')).toBe('1')
    // Decoded dimensions that contradict the header (sniffer bug / exotic
    // file): the reservation must self-heal back to natural sizing.
    Object.defineProperty(img, 'naturalWidth', { value: 100, configurable: true })
    Object.defineProperty(img, 'naturalHeight', { value: 100, configurable: true })
    img.dispatchEvent(new Event('load'))
    expect(img.getAttribute('data-size-reserved')).toBeNull()
  })

  it('keeps the reservation when the decoded image matches (within rounding slack)', () => {
    const parsed = makeMcpToolResult([
      { type: 'image', mimeType: 'image/png', data: pngBase64(640, 480) },
    ])
    const { container } = renderClaudeToolResult(parsed, {
      spanType: 'mcp__playwright__screenshot',
    })
    const img = container.querySelector('img')!
    Object.defineProperty(img, 'naturalWidth', { value: 640, configurable: true })
    Object.defineProperty(img, 'naturalHeight', { value: 480, configurable: true })
    img.dispatchEvent(new Event('load'))
    expect(img.getAttribute('data-size-reserved')).toBe('1')
  })

  it('renders external image URLs as a link (not inlined)', () => {
    const parsed = makeMcpToolResult([
      { type: 'image', url: 'https://example.com/screenshot.png' },
    ])
    const { container } = renderClaudeToolResult(parsed, {
      spanType: 'mcp__playwright__screenshot',
    })
    expect(container.querySelector('img')).toBeNull()
    const link = container.querySelector('a')
    expect(link).not.toBeNull()
    expect(link?.getAttribute('href')).toBe('https://example.com/screenshot.png')
    expect(link?.getAttribute('target')).toBe('_blank')
  })

  it('falls back to a placeholder for image blocks with no data and no URL', () => {
    const parsed = makeMcpToolResult([
      { type: 'image', mimeType: 'image/png' },
    ])
    const { container } = renderClaudeToolResult(parsed, {
      spanType: 'mcp__playwright__screenshot',
    })
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent ?? '').toContain('[image: image/png]')
  })

  it('falls back to a placeholder for unsupported MIME types (e.g. svg)', () => {
    const parsed = makeMcpToolResult([
      { type: 'image', mimeType: 'image/svg+xml', data: '<svg/>' },
    ])
    const { container } = renderClaudeToolResult(parsed, {
      spanType: 'mcp__server__svg',
    })
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent ?? '').toContain('unsupported format')
  })
})

// ---------------------------------------------------------------------------
// Codex mcpToolCall / dynamicToolCall rendering
// ---------------------------------------------------------------------------

describe('codex mcpToolCall rendering', () => {
  it('renders server/tool header and result content', () => {
    const { container } = renderCodexItem({
      type: 'mcpToolCall',
      server: 'tavily',
      tool: 'tavily_search',
      status: 'completed',
      arguments: { query: 'rust' },
      result: {
        content: [
          { type: 'text', text: 'Mcp tavily search body' },
        ],
        structuredContent: null,
      },
    })
    const text = container.textContent ?? ''
    expect(text).toContain('tavily / tavily_search')
    expect(text).toContain('Mcp tavily search body')
    // Args still show in the body for inspection.
    expect(text).toContain('"query"')
  })

  it('renders the failed status badge and error message', () => {
    const { container } = renderCodexItem({
      type: 'mcpToolCall',
      server: 'gh',
      tool: 'create_issue',
      status: 'failed',
      arguments: {},
      result: null,
      error: { message: 'Authorization required' },
    })
    const text = container.textContent ?? ''
    expect(text).toContain('failed')
    expect(text).toContain('Authorization required')
  })

  it('renders a structuredContent section when present', () => {
    const { container } = renderCodexItem({
      type: 'mcpToolCall',
      server: 's',
      tool: 't',
      status: 'completed',
      result: {
        content: [],
        structuredContent: { count: 42 },
      },
    })
    const text = container.textContent ?? ''
    expect(text).toContain('Structured')
    expect(text).toContain('"count"')
    expect(text).toContain('42')
  })
})

describe('codex dynamicToolCall rendering', () => {
  it('renders namespace as the server label and inputText content', () => {
    const { container } = renderCodexItem({
      type: 'dynamicToolCall',
      namespace: 'openai',
      tool: 'browser',
      status: 'completed',
      arguments: { url: 'https://example.com' },
      contentItems: [
        { type: 'inputText', text: 'Page title: Example' },
      ],
    })
    const text = container.textContent ?? ''
    expect(text).toContain('openai / browser')
    expect(text).toContain('Page title: Example')
  })

  it('renders inputImage items with http URLs as a link (not inlined)', () => {
    const { container } = renderCodexItem({
      type: 'dynamicToolCall',
      namespace: '',
      tool: 'screenshot',
      status: 'completed',
      contentItems: [{ type: 'inputImage', imageUrl: 'https://example.com/x.png' }],
    })
    expect(container.querySelector('img')).toBeNull()
    const link = container.querySelector('a')
    expect(link).not.toBeNull()
    expect(link?.getAttribute('href')).toBe('https://example.com/x.png')
  })

  it('renders inline data: URL inputImage items directly', () => {
    const { container } = renderCodexItem({
      type: 'dynamicToolCall',
      namespace: '',
      tool: 'screenshot',
      status: 'completed',
      contentItems: [{ type: 'inputImage', imageUrl: 'data:image/png;base64,iVBORw0KGgo=' }],
    })
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    expect(img?.getAttribute('src')).toBe('data:image/png;base64,iVBORw0KGgo=')
  })
})
