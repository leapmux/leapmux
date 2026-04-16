import type { MessageCategory } from '../messageClassification'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import './testMocks'

const { renderMessageContent } = await import('../messageRenderers')
type RenderContext = import('../messageRenderers').RenderContext

/** Construct a ToolSearch tool_use assistant message. */
function makeToolSearchMessage(input: Record<string, unknown> = {}) {
  return {
    type: 'assistant',
    message: {
      content: [{
        type: 'tool_use',
        id: 'test-toolsearch',
        name: 'ToolSearch',
        input: { query: 'select:Read,Glob,Grep', ...input },
      }],
    },
  }
}

/** Construct a ToolSearch tool_result user message. */
function makeToolSearchResult(
  toolRefs: string[],
  toolUseResult?: Record<string, unknown>,
) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [{
        tool_use_id: 'test-toolsearch',
        type: 'tool_result',
        content: toolRefs.map(name => ({ type: 'tool_reference', tool_name: name })),
      }],
    },
    tool_use_result: toolUseResult,
  }
}

/** Render a ToolSearch tool_use message and return its text content. */
function renderToolUseText(input?: Record<string, unknown>, context?: RenderContext): string {
  const msg = makeToolSearchMessage(input)
  const toolUse = (msg.message.content as Array<Record<string, unknown>>)[0]
  const category: MessageCategory = { kind: 'tool_use', toolName: 'ToolSearch', toolUse, content: msg.message.content as Array<Record<string, unknown>> }
  const result = renderMessageContent(msg, MessageRole.ASSISTANT, context, category, AgentProvider.CLAUDE_CODE)
  const { container } = render(() => result)
  return container.textContent?.trim() ?? ''
}

/** Render a ToolSearch tool_result message and return its container element. */
function renderToolResultContainer(
  toolRefs: string[],
  toolUseResult?: Record<string, unknown>,
  context?: RenderContext,
): HTMLElement {
  const msg = makeToolSearchResult(toolRefs, toolUseResult)
  const category: MessageCategory = { kind: 'tool_result' }
  const result = renderMessageContent(msg, MessageRole.USER, context, category, AgentProvider.CLAUDE_CODE)
  const { container } = render(() => result)
  return container
}

describe('toolSearch tool_use rendering', () => {
  it('shows query string in header', () => {
    const text = renderToolUseText()
    expect(text).toContain('"select:Read,Glob,Grep"')
  })

  it('shows custom query string', () => {
    const text = renderToolUseText({ query: 'slack message' })
    expect(text).toContain('"slack message"')
  })

  it('renders without error when query is missing', () => {
    const text = renderToolUseText({ query: undefined })
    expect(text).toContain('ToolSearch')
  })

  it('shows query with max_results', () => {
    const text = renderToolUseText({ query: 'notebook', max_results: 3 })
    expect(text).toContain('"notebook"')
  })

  it('shows only query with no result count summary (child data fields removed)', () => {
    const text = renderToolUseText()
    expect(text).toContain('"select:Read,Glob,Grep"')
    expect(text).not.toContain('Found')
  })
})

describe('toolSearch tool_result rendering', () => {
  it('shows matched tool names', () => {
    const container = renderToolResultContainer(
      ['Read', 'Glob', 'Grep', 'Bash'],
      {
        tool_name: 'ToolSearch',
        matches: ['Read', 'Glob', 'Grep', 'Bash'],
        query: 'select:Read,Glob,Grep,Bash',
        total_deferred_tools: 19,
      },
    )
    const text = container.textContent ?? ''
    expect(text).toContain('Read')
    expect(text).toContain('Glob')
    expect(text).toContain('Grep')
    expect(text).toContain('Bash')
  })

  it('shows single matched tool', () => {
    const container = renderToolResultContainer(
      ['Edit'],
      {
        tool_name: 'ToolSearch',
        matches: ['Edit'],
        query: 'select:Edit',
        total_deferred_tools: 19,
      },
    )
    const text = container.textContent ?? ''
    expect(text).toContain('Edit')
  })

  it('shows "No tools found" when matches is empty', () => {
    const container = renderToolResultContainer(
      [],
      {
        tool_name: 'ToolSearch',
        matches: [],
        query: 'nonexistent',
        total_deferred_tools: 19,
      },
    )
    const text = container.textContent ?? ''
    expect(text).toContain('No tools found')
  })
})
