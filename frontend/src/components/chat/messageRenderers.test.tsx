import type { RenderContext } from './messageRenderers'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { ContentCompression } from '~/generated/leapmux/v1/agent_pb'
import { renderMessageContent } from './messageRenderers'

// Mock shiki worker to avoid Web Worker unavailability in test environment.
// Vitest auto-hoists vi.mock calls above imports.
vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: vi.fn().mockResolvedValue(null),
}))

/** Build a tool_use assistant message for the given tool name and input. */
function makeToolUseMessage(name: string, input: Record<string, unknown>) {
  return {
    type: 'assistant',
    message: {
      content: [{ type: 'tool_use', id: 'test-id', name, input }],
    },
  }
}

/** Render a tool_use message and return the trimmed text content. */
function renderToolUseText(name: string, input: Record<string, unknown>, context?: RenderContext): string {
  const parsed = makeToolUseMessage(name, input)
  const result = renderMessageContent(parsed, 2 /* ASSISTANT */, context)
  const { container } = render(() => result)
  return container.textContent?.trim() ?? ''
}

describe('skill renderer', () => {
  it('renders Skill: /create-pr', () => {
    expect(renderToolUseText('Skill', { skill: 'create-pr' })).toBe('Skill: /create-pr')
  })

  it('renders Skill: /review-pr', () => {
    expect(renderToolUseText('Skill', { skill: 'review-pr' })).toBe('Skill: /review-pr')
  })

  it('renders Skill: /commit', () => {
    expect(renderToolUseText('Skill', { skill: 'commit' })).toBe('Skill: /commit')
  })
})

describe('agent/task renderer', () => {
  it('renders Agent with description only', () => {
    expect(renderToolUseText('Agent', { description: 'Search codebase' }))
      .toBe('Search codebase')
  })

  it('renders Task with description only', () => {
    expect(renderToolUseText('Task', { description: 'Run tests' }))
      .toBe('Run tests')
  })

  it('renders Agent with description and subagent_type', () => {
    expect(renderToolUseText('Agent', { description: 'Search', subagent_type: 'Explore' }))
      .toBe('Search (Explore)')
  })

  it('falls back to tool name when description is missing', () => {
    expect(renderToolUseText('Agent', {})).toBe('Agent')
    expect(renderToolUseText('Task', {})).toBe('Task')
  })

  it('shows no status (child data fields removed)', () => {
    const text = renderToolUseText('Agent', { description: 'Analyze code' })
    expect(text).toContain('Analyze code')
    expect(text).not.toContain('Running')
    expect(text).not.toContain('Complete')
    expect(text).not.toContain('Failed')
  })

  it('renders description + subagent_type in title without status', () => {
    const text = renderToolUseText('Agent', { description: 'Fix bug', subagent_type: 'code' })
    expect(text).toContain('Fix bug (code)')
    expect(text).not.toContain('Complete')
  })

  it('formats title as "SubAgent: rest" when description starts with subagent name', () => {
    const text = renderToolUseText('Agent', { description: 'Explore message classification', subagent_type: 'Explore' })
    expect(text).toContain('Explore: message classification')
  })

  it('does not format title when description does not start with subagent name', () => {
    const text = renderToolUseText('Agent', { description: 'Search codebase', subagent_type: 'Explore' })
    expect(text).toContain('Search codebase')
    expect(text).not.toContain('Explore:')
  })

  it('shows only description without stats (child data fields removed)', () => {
    const text = renderToolUseText('Agent', { description: 'Search' })
    expect(text).toBe('Search')
    expect(text).not.toContain('tokens')
    expect(text).not.toContain('tool uses')
  })
})

/** Build a fake AgentChatMessage with JSON content (uncompressed). */
function makeFakeMessage(content: Record<string, unknown>): AgentChatMessage {
  return {
    content: new TextEncoder().encode(JSON.stringify(content)),
    contentCompression: ContentCompression.NONE,
  } as unknown as AgentChatMessage
}

describe('write tool_use hides content when linked result is an update', () => {
  const writeInput = { file_path: '/tmp/test.go', content: 'package main\n\nfunc main() {}\n' }

  it('shows diff when no linked tool_result exists', () => {
    const { container } = render(() =>
      renderMessageContent(makeToolUseMessage('Write', writeInput), 2 /* ASSISTANT */),
    )
    // The diff view should render the new file content.
    expect(container.textContent).toContain('package main')
  })

  it('hides diff when linked tool_result has type "update"', () => {
    const toolResultMessage = makeFakeMessage({
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', content: 'Updated successfully.' }] },
      tool_use_result: {
        type: 'update',
        filePath: '/tmp/test.go',
        structuredPatch: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-old', '+new'] }],
      },
    })
    const context: RenderContext = { toolResultMessage }
    const { container } = render(() =>
      renderMessageContent(makeToolUseMessage('Write', writeInput), 2 /* ASSISTANT */, context),
    )
    // The diff should be hidden — the tool_result shows the diff instead.
    expect(container.textContent).not.toContain('package main')
  })

  it('shows diff when linked tool_result is not an update (new file)', () => {
    const toolResultMessage = makeFakeMessage({
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', content: 'File created.' }] },
      tool_use_result: { filePath: '/tmp/test.go' },
    })
    const context: RenderContext = { toolResultMessage }
    const { container } = render(() =>
      renderMessageContent(makeToolUseMessage('Write', writeInput), 2 /* ASSISTANT */, context),
    )
    // New file creation: the full content should still be visible.
    expect(container.textContent).toContain('package main')
  })
})

/** Build a Read tool_result message without structured tool_use_result. */
function makeReadToolResult(resultContent: string, context?: Partial<RenderContext>) {
  const parsed = {
    type: 'user',
    message: {
      role: 'user',
      content: [{
        tool_use_id: 'test-read',
        type: 'tool_result',
        content: resultContent,
      }],
    },
  }
  return {
    parsed,
    render: () => {
      const result = renderMessageContent(parsed, 1 /* USER */, { spanType: 'Read', ...context })
      const { container } = render(() => result)
      return container
    },
  }
}

describe('read tool_result without structured data renders as ReadResultView', () => {
  it('renders tab-delimited content with line numbers', () => {
    const container = makeReadToolResult('1\tfoo\n2\tbar\n3\tbaz').render()
    // ReadFileResultView renders line numbers as distinct elements.
    expect(container.textContent).toContain('1')
    expect(container.textContent).toContain('foo')
    expect(container.textContent).toContain('bar')
    expect(container.textContent).toContain('baz')
    // Should use codeViewContainer (ReadResultView), not toolResultContentPre.
    expect(container.querySelector('[class*="codeView"]')).not.toBeNull()
  })

  it('strips [result-id: ...] suffix and still renders as ReadResultView', () => {
    const container = makeReadToolResult('1\tfoo\n2\tbar\n\n[result-id: r7]').render()
    expect(container.querySelector('[class*="codeView"]')).not.toBeNull()
    expect(container.textContent).toContain('foo')
    expect(container.textContent).not.toContain('result-id')
  })

  it('falls back to preformatted text for non-parseable content', () => {
    const container = makeReadToolResult('this is not cat-n output').render()
    // Should render as ToolResultMessage (pre text), not ReadResultView.
    expect(container.querySelector('[class*="codeView"]')).toBeNull()
    expect(container.textContent).toContain('this is not cat-n output')
  })
})
