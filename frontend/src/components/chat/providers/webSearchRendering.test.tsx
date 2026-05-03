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

function makeResult(toolUseResult: Record<string, unknown> | undefined, content: string) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [{ type: 'tool_result', tool_use_id: 'r1', content }],
    },
    ...(toolUseResult ? { tool_use_result: toolUseResult } : {}),
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

describe('claude WebSearch tool_result rendering', () => {
  it('renders link list and summary count', () => {
    const parsed = makeResult({
      tool_name: 'WebSearch',
      query: 'react hooks',
      results: [
        { content: [
          { url: 'https://search.example.com/a', title: 'WebSearchTitleA' },
          { url: 'https://search.example.com/b', title: 'WebSearchTitleB' },
        ] },
        'final markdown summary',
      ],
    }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'WebSearch' })
    const text = container.textContent ?? ''
    expect(text).toContain('2 results')
    expect(text).toContain('WebSearchTitleA')
    expect(text).toContain('WebSearchTitleB')
  })
})

describe('codex webSearch action rendering', () => {
  it('renders the "Searching the web" placeholder for an empty other action', () => {
    const { container } = renderCodexItem({
      type: 'webSearch',
      action: { type: 'other' },
      query: '',
    })
    expect(container.textContent ?? '').toContain('Searching the web')
  })

  it('renders a search action with the leading query', () => {
    const { container } = renderCodexItem({
      type: 'webSearch',
      action: { type: 'search', query: 'codex web search query' },
    })
    expect(container.textContent ?? '').toContain('codex web search query')
  })

  it('renders an openPage action and uses the WebFetch tool name', () => {
    const { container } = renderCodexItem({
      type: 'webSearch',
      action: { type: 'openPage', url: 'https://codex.example.com/page' },
    })
    expect(container.textContent ?? '').toContain('codex.example.com')
  })
})
