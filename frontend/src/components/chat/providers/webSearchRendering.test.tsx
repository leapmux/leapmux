import type { MessageCategory } from '../messageClassifier'
import type { MessageContentRenderContext } from '~/test-support/messageContentRenderer'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import './claude/plugin'
import './codex/plugin'
import '~/components/chat/providers'

const { renderMessageContent } = await import('~/test-support/messageContentRenderer')

function renderClaudeToolResult(parsed: Record<string, unknown>, context?: MessageContentRenderContext) {
  const category: MessageCategory = { kind: 'tool_result' }
  const result = renderMessageContent(parsed, context, category, AgentProvider.CLAUDE_CODE)
  return render(() => result)
}

function makeResult(toolUseResult: Record<string, unknown> | undefined, content: string) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [
        { type: 'tool_result', tool_use_id: 'r1', content },
      ],
    },
    ...(toolUseResult
      ? { tool_use_result: toolUseResult }
      : {}),
  }
}

function renderCodexItem(item: Record<string, unknown>, context?: MessageContentRenderContext) {
  const parsed = { item, threadId: 't1', turnId: 'r1' }
  const category: MessageCategory = { kind: 'tool_use' }
  const result = renderMessageContent(parsed, context, category, AgentProvider.CODEX)
  return render(() => result)
}

describe('claude WebSearch tool_result rendering', () => {
  it('renders link list and summary count', () => {
    const parsed = makeResult({
      tool_name: 'WebSearch',
      query: 'react hooks',
      results: [
        {
          content: [
            { url: 'https://search.example.com/a', title: 'WebSearchTitleA' },
            { url: 'https://search.example.com/b', title: 'WebSearchTitleB' },
          ],
        },
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

function renderAntigravitySearchWeb(parsed: Record<string, unknown>, context?: MessageContentRenderContext) {
  const category: MessageCategory = { kind: 'tool_result' }
  const result = renderMessageContent(parsed, context, category, AgentProvider.ANTIGRAVITY_CLI)
  return render(() => result)
}

describe('antigravity searchWeb rendering', () => {
  const payload = {
    type: 'CORTEX_STEP_TYPE_SEARCH_WEB',
    status: 'CORTEX_STEP_STATUS_DONE',
    metadata: {
      toolCall: {
        id: 'call_ag_search_1',
        name: 'search_web',
        argumentsJson: JSON.stringify({ query: 'windows credential blob rust' }),
      },
    },
    searchWeb: {
      query: 'windows credential blob rust',
      searchType: 'SEARCH_WEB_TYPE_WEB',
      summary: '### Findings\n\nUse [Reference](https://ref.example.com) to decode UTF-16LE.',
      thirdPartyConfig: {
        provider: 'THIRD_PARTY_WEB_SEARCH_PROVIDER_GEMINI',
      },
      webDocuments: [
        { documentId: '1', title: 'Alpha Guide', url: 'https://alpha.example.com/1' },
        { documentId: '2', title: 'Beta Docs', url: 'https://beta.example.com/2' },
        { documentId: '3', title: 'Gamma Notes', url: 'https://gamma.example.com/3' },
        { documentId: '4', title: 'Delta Spec', url: 'https://delta.example.com/4' },
        { documentId: '5', title: 'Epsilon Post', url: 'https://epsilon.example.com/5' },
      ],
    },
  }

  it('renders the query, result count, first three links with hrefs and domains, and hides the summary when collapsed', () => {
    const { container } = renderAntigravitySearchWeb(payload, { spanType: 'antigravity_step' })
    const text = container.textContent ?? ''
    expect(text).toContain('windows credential blob rust')
    expect(text).toContain('5 results')
    expect(text).toContain('Alpha Guide')
    expect(text).toContain('Beta Docs')
    expect(text).toContain('Gamma Notes')
    expect(text).not.toContain('Delta Spec')
    expect(text).not.toContain('Epsilon Post')
    expect(text).toContain('alpha.example.com')
    expect(text).toContain('beta.example.com')
    expect(text).toContain('gamma.example.com')
    expect(text).not.toContain('Findings')

    const anchors = Array.from(container.querySelectorAll('a'))
    expect(anchors.map(anchor => ({
      text: anchor.textContent,
      href: anchor.getAttribute('href'),
    }))).toEqual([
      { text: 'Alpha Guide', href: 'https://alpha.example.com/1' },
      { text: 'Beta Docs', href: 'https://beta.example.com/2' },
      { text: 'Gamma Notes', href: 'https://gamma.example.com/3' },
    ])
  })

  it('renders all five links and the markdown summary when expanded', async () => {
    const { container } = renderAntigravitySearchWeb(payload, {
      spanType: 'antigravity_step',
      getMessageUiState: () => true,
    })
    await settleWorkerReplies()

    const text = container.textContent ?? ''
    expect(text).toContain('5 results')
    expect(text).toContain('Alpha Guide')
    expect(text).toContain('Beta Docs')
    expect(text).toContain('Gamma Notes')
    expect(text).toContain('Delta Spec')
    expect(text).toContain('Epsilon Post')
    expect(container.querySelector('h3')?.textContent).toBe('Findings')
    const summaryLink = container.querySelector('a[href="https://ref.example.com"]')
    expect(summaryLink?.textContent).toBe('Reference')
  })
})
