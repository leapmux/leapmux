import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'

// Mock renderAnsi to avoid shiki initialization in tests.
vi.mock('~/lib/renderAnsi', () => ({
  // eslint-disable-next-line no-control-regex -- ANSI escape detection requires matching control characters
  containsAnsi: (text: string) => /\x1B\[[\d;]*m/.test(text),
  renderAnsi: (text: string) => `<pre class="shiki"><code>${text}</code></pre>`,
}))

// Mock renderMarkdown to avoid shiki initialization in tests.
vi.mock('~/lib/renderMarkdown', () => ({
  renderMarkdown: (text: string) => text,
}))

const { renderMessageContent } = await import('./messageRenderers')
type RenderContext = import('./messageRenderers').RenderContext

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
  const result = renderMessageContent(msg, 2 /* ASSISTANT */, context)
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
  const result = renderMessageContent(msg, 1 /* USER */, context)
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

  it('shows "Found N tools" summary when matches available', () => {
    const text = renderToolUseText(undefined, {
      childToolSearchMatches: ['Read', 'Glob', 'Grep'],
    })
    expect(text).toContain('Found 3 tools')
  })

  it('shows "Found 1 tool" for singular match', () => {
    const text = renderToolUseText(undefined, {
      childToolSearchMatches: ['Edit'],
    })
    expect(text).toContain('Found 1 tool')
  })
})

describe('toolSearch tool_result rendering', () => {
  it('shows matched tool names as list items', () => {
    const container = renderToolResultContainer(
      ['Read', 'Glob', 'Grep', 'Bash'],
      {
        tool_name: 'ToolSearch',
        matches: ['Read', 'Glob', 'Grep', 'Bash'],
        query: 'select:Read,Glob,Grep,Bash',
        total_deferred_tools: 19,
      },
    )
    const listItems = container.querySelectorAll('li')
    expect(listItems.length).toBe(4)
    expect(listItems[0].textContent).toBe('Read')
    expect(listItems[1].textContent).toBe('Glob')
    expect(listItems[2].textContent).toBe('Grep')
    expect(listItems[3].textContent).toBe('Bash')
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
    const listItems = container.querySelectorAll('li')
    expect(listItems.length).toBe(1)
    expect(listItems[0].textContent).toBe('Edit')
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
