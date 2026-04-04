import type { MessageCategory } from '../messageClassification'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider, MessageRole } from '~/generated/leapmux/v1/agent_pb'

// Mock renderMarkdown to avoid shiki initialization in tests.
vi.mock('~/lib/renderMarkdown', () => ({
  renderMarkdown: (text: string) => text,
  shikiHighlighter: { codeToHtml: (code: string) => `<pre><code>${code}</code></pre>` },
}))

// eslint-disable-next-line no-control-regex -- ANSI escape detection requires matching control characters
const ANSI_ESCAPE_RE = /\x1B\[[\d;]*m/

vi.mock('~/lib/renderAnsi', () => ({
  containsAnsi: (text: string) => ANSI_ESCAPE_RE.test(text),
  renderAnsi: (text: string) => `<pre class="shiki"><code>${text}</code></pre>`,
}))

const { renderMessageContent } = await import('../messageRenderers')

// ---------------------------------------------------------------------------
// MCP name parsing (unit tests for inline utilities via rendered output)
// ---------------------------------------------------------------------------

/** Construct an MCP tool_use assistant message. */
function makeMcpToolUse(toolName: string, input: Record<string, unknown> = {}) {
  return {
    type: 'assistant',
    message: {
      content: [{
        type: 'tool_use',
        id: 'toolu_test',
        name: toolName,
        input,
      }],
    },
  }
}

/** Construct an MCP tool_result user message. */
function makeMcpToolResult(content: string) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [{
        type: 'tool_result',
        tool_use_id: 'toolu_test',
        content,
      }],
    },
  }
}

const toolUseCategory: MessageCategory = {
  kind: 'tool_use',
  toolName: 'mcp__claude_ai_Tavily__tavily_research',
  toolUse: { name: 'mcp__claude_ai_Tavily__tavily_research', input: { input: 'Go OIDC libraries comparison' } },
  content: [],
}

const toolResultCategory: MessageCategory = { kind: 'tool_result' }

describe('mcp tool_use rendering', () => {
  it('should render mcp tool_use with humanized server/tool name', () => {
    const parsed = makeMcpToolUse('mcp__claude_ai_Tavily__tavily_research', {
      input: 'Go OIDC libraries comparison',
      model: 'pro',
    })

    const { container } = render(() =>
      renderMessageContent(parsed, MessageRole.ASSISTANT, { spanType: 'mcp__claude_ai_Tavily__tavily_research' }, toolUseCategory, AgentProvider.CLAUDE_CODE),
    )

    const text = container.textContent || ''
    // Should show humanized server name and tool name
    expect(text).toContain('Claude Ai Tavily')
    expect(text).toContain('tavily_research')
    // Should show the first input string as a hint
    expect(text).toContain('Go OIDC libraries comparison')
  })

  it('should render mcp tool_use with double-underscore in tool name', () => {
    const parsed = makeMcpToolUse('mcp__github__search__repos', {
      query: 'golang oidc',
    })

    const category: MessageCategory = {
      kind: 'tool_use',
      toolName: 'mcp__github__search__repos',
      toolUse: { name: 'mcp__github__search__repos', input: { query: 'golang oidc' } },
      content: [],
    }

    const { container } = render(() =>
      renderMessageContent(parsed, MessageRole.ASSISTANT, { spanType: 'mcp__github__search__repos' }, category, AgentProvider.CLAUDE_CODE),
    )

    const text = container.textContent || ''
    // Server is "github", tool is "search__repos" (preserves __ in tool name)
    expect(text).toContain('Github')
    expect(text).toContain('search__repos')
  })

  it('should prefer common param names for hint over iteration order', () => {
    // "model" comes first in insertion order, but "input" is a preferred hint key
    const parsed = makeMcpToolUse('mcp__claude_ai_Tavily__tavily_research', {
      model: 'pro',
      input: 'Go OIDC libraries comparison',
    })

    const category: MessageCategory = {
      kind: 'tool_use',
      toolName: 'mcp__claude_ai_Tavily__tavily_research',
      toolUse: { name: 'mcp__claude_ai_Tavily__tavily_research', input: { model: 'pro', input: 'Go OIDC libraries comparison' } },
      content: [],
    }

    const { container } = render(() =>
      renderMessageContent(parsed, MessageRole.ASSISTANT, { spanType: 'mcp__claude_ai_Tavily__tavily_research' }, category, AgentProvider.CLAUDE_CODE),
    )

    const text = container.textContent || ''
    // Should show "input" value, not "model" value
    expect(text).toContain('Go OIDC libraries comparison')
    expect(text).not.toContain('"pro"')
  })

  it('should render unknown non-MCP tools with tool name', () => {
    const parsed = makeMcpToolUse('SomeNewTool', {
      description: 'Do something',
    })

    const category: MessageCategory = {
      kind: 'tool_use',
      toolName: 'SomeNewTool',
      toolUse: { name: 'SomeNewTool', input: { description: 'Do something' } },
      content: [],
    }

    const { container } = render(() =>
      renderMessageContent(parsed, MessageRole.ASSISTANT, { spanType: 'SomeNewTool' }, category, AgentProvider.CLAUDE_CODE),
    )

    const text = container.textContent || ''
    expect(text).toContain('SomeNewTool')
    expect(text).toContain('Do something')
  })

  it('should render regular tools normally (not as MCP)', () => {
    const parsed = makeMcpToolUse('Bash', {
      description: 'List files',
      command: 'ls -la',
    })

    const category: MessageCategory = {
      kind: 'tool_use',
      toolName: 'Bash',
      toolUse: { name: 'Bash', input: { description: 'List files', command: 'ls -la' } },
      content: [],
    }

    const { container } = render(() =>
      renderMessageContent(parsed, MessageRole.ASSISTANT, { spanType: 'Bash' }, category, AgentProvider.CLAUDE_CODE),
    )

    const text = container.textContent || ''
    // Should render as Bash, not as MCP
    expect(text).toContain('List files')
    expect(text).not.toContain('mcp')
  })
})

describe('mcp tool_result rendering', () => {
  it('should render mcp tool_result as markdown', () => {
    const parsed = makeMcpToolResult('# Research Report\n\nThis is a comparison of Go OIDC libraries.')

    const { container } = render(() =>
      renderMessageContent(parsed, MessageRole.USER, { spanType: 'mcp__claude_ai_Tavily__tavily_research' }, toolResultCategory, AgentProvider.CLAUDE_CODE),
    )

    const text = container.textContent || ''
    // Should render the markdown content (renderMarkdown is mocked to pass through)
    expect(text).toContain('Research Report')
    expect(text).toContain('comparison of Go OIDC libraries')
  })
})
