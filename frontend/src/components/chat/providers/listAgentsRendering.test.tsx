import type { MessageCategory } from '../messageClassifier'
import type { MessageContentRenderContext } from '../messageContentRenderer'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolMeta } from '~/test-support/toolCallFixture'
import './testMocks'

const { renderMessageContent } = await import('../messageContentRenderer')

const LISTING = [
  '| Name | Kind | Status |',
  '|------|------|--------|',
  '| Explore the parser | in-process subagent | completed |',
  '| laptop | Remote Control session | idle |',
].join('\n')

function renderToolUseText(input: Record<string, unknown>): string {
  const msg = {
    type: 'assistant',
    message: {
      content: [{ type: 'tool_use', id: 'test-listagents', name: 'ListAgents', input }],
    },
  }
  const category: MessageCategory = { kind: 'tool_use' }
  const result = renderMessageContent(msg, undefined, category, AgentProvider.CLAUDE_CODE)
  return render(() => result).container.textContent?.trim() ?? ''
}

function listAgentsToolResult(resultContent: string, toolUseResult?: Record<string, unknown>) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [{ tool_use_id: 'test-listagents', type: 'tool_result', content: resultContent }],
    },
    ...(toolUseResult ? { tool_use_result: toolUseResult } : {}),
  }
}

function renderToolResult(
  resultContent: string,
  toolUseResult?: Record<string, unknown>,
  context?: MessageContentRenderContext,
): HTMLElement {
  const msg = listAgentsToolResult(resultContent, toolUseResult)
  const category: MessageCategory = { kind: 'tool_result' }
  const result = renderMessageContent(
    msg,
    { spanType: 'ListAgents', ...context },
    category,
    AgentProvider.CLAUDE_CODE,
  )
  return render(() => result).container
}

describe('claude ListAgents tool_use rendering', () => {
  // Both filters are optional and absent in this CLI build, so the bare label is
  // the normal case rather than a degraded one.
  it('labels an unfiltered call', () => {
    expect(renderToolUseText({})).toBe('List agents')
  })

  it('lists the filters when the call carries them', () => {
    expect(renderToolUseText({ channel: 'team' })).toContain('channel: team')
    expect(renderToolUseText({ q: 'explore' })).toContain('matching: explore')
    const both = renderToolUseText({ channel: 'team', q: 'explore' })
    expect(both).toContain('channel: team')
    expect(both).toContain('matching: explore')
  })
})

describe('claude ListAgents tool_result rendering', () => {
  // The CLI hands the whole listing over as ONE pre-formatted string under
  // `listing`, and maps that same string into the block content.
  it('renders the structured listing', () => {
    const container = renderToolResult('', { listing: LISTING })
    expect(container.textContent).toContain('Explore the parser')
    expect(container.textContent).toContain('Remote Control session')
  })

  it('falls back to the block content when no structured payload is present', () => {
    const container = renderToolResult(LISTING)
    expect(container.textContent).toContain('Explore the parser')
  })

  it('prefers the structured listing over the block content', () => {
    const container = renderToolResult('stale text', { listing: LISTING })
    expect(container.textContent).toContain('Explore the parser')
    expect(container.textContent).not.toContain('stale text')
  })

  // Returning null from the dispatch entry hands the row to the catch-all, which
  // is how every renderer here degrades rather than rendering an empty card.
  it('falls through to the catch-all when the payload carries no listing', () => {
    const container = renderToolResult('', { unexpected: 1 })
    expect(container.textContent ?? '').not.toContain('Reachable agents')
  })
})

describe('claude toolbar actions for ListAgents', () => {
  // The toolbar's Copy must act on the text the body shows, which is the structured
  // listing whenever there is one.
  it('copies the listing the card drew', () => {
    const meta = providerToolMeta(AgentProvider.CLAUDE_CODE, listAgentsToolResult('', { listing: LISTING }), { spanType: 'ListAgents' })
    expect(meta?.copyableContent()).toBe(LISTING)
  })

  // A listing draws as a MARKDOWN body, which the row never clamps -- the table is
  // the answer, and half a table states nothing. So the toolbar offers no Expand,
  // whatever the listing's length. The rule this replaced counted the listing's
  // lines and drew a button that revealed nothing.
  it.each([
    ['a long listing', LISTING],
    ['a one-line listing', 'No agents are reachable.'],
  ])('offers no expand for %s', (_name, listing) => {
    const meta = providerToolMeta(AgentProvider.CLAUDE_CODE, listAgentsToolResult('', { listing }), { spanType: 'ListAgents' })
    expect(meta?.collapsible).toBe(false)
  })

  // The renderer treats a whitespace-only listing as absent and hands the row to
  // the catch-all. The toolbar has to agree: reading the untrimmed value made it
  // offer a Copy button on a card whose body came from somewhere else, and yield
  // spaces.
  it('offers no Copy for a whitespace-only listing', () => {
    const meta = providerToolMeta(AgentProvider.CLAUDE_CODE, listAgentsToolResult('', { listing: '   \n\t  ' }), { spanType: 'ListAgents' })
    expect(meta?.hasCopyable).toBe(false)
    expect(meta?.copyableContent()).toBeNull()
  })
})
