import type { MessageCategory } from '../messageClassification'
import type { RenderContext } from '../messageRenderers'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { claudeToolResultMeta } from './claude/toolResult'
import './testMocks'

const { renderMessageContent } = await import('../messageRenderers')

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
  const content = msg.message.content as Array<Record<string, unknown>>
  const category: MessageCategory = {
    kind: 'tool_use',
    toolName: 'ListAgents',
    toolUse: content[0],
    content,
  }
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
  context?: RenderContext,
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

describe('claudeToolResultMeta for ListAgents', () => {
  // The toolbar's expand button and Copy must act on the text the body shows,
  // which is the structured listing whenever there is one.
  it('reports a long listing as collapsible and copies it', () => {
    const meta = claudeToolResultMeta(
      { kind: 'tool_result' },
      listAgentsToolResult('', { listing: LISTING }),
      'ListAgents',
      undefined,
    )
    expect(meta?.collapsible).toBe(true)
    expect(meta?.copyableContent()).toBe(LISTING)
  })

  it('reports a one-line listing as not collapsible', () => {
    const meta = claudeToolResultMeta(
      { kind: 'tool_result' },
      listAgentsToolResult('', { listing: 'No agents are reachable.' }),
      'ListAgents',
      undefined,
    )
    expect(meta?.collapsible).toBe(false)
  })

  // The renderer treats a whitespace-only listing as absent and hands the row to
  // the catch-all. The toolbar has to agree: reading the untrimmed value made it
  // offer a Copy button on a card whose body came from somewhere else, and yield
  // spaces.
  it('offers no Copy for a whitespace-only listing', () => {
    const meta = claudeToolResultMeta(
      { kind: 'tool_result' },
      listAgentsToolResult('', { listing: '   \n\t  ' }),
      'ListAgents',
      undefined,
    )
    expect(meta?.hasCopyable).toBe(false)
    expect(meta?.copyableContent()).toBeNull()
  })
})
