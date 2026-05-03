import type { MessageCategory } from './messageClassification'
import type { RenderContext } from './messageRenderers'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider, ContentCompression } from '~/generated/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { renderMessageContent } from './messageRenderers'
import './providers'

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

/** Build a tool_use category for dispatch. */
function makeToolUseCategory(name: string, input: Record<string, unknown>): MessageCategory {
  const toolUse = { type: 'tool_use' as const, id: 'test-id', name, input }
  return { kind: 'tool_use', toolName: name, toolUse, content: [toolUse] }
}

/** Render a tool_use message and return the trimmed text content. */
function renderToolUseText(name: string, input: Record<string, unknown>, context?: RenderContext): string {
  const parsed = makeToolUseMessage(name, input)
  const category = makeToolUseCategory(name, input)
  const result = renderMessageContent(parsed, context, category, AgentProvider.CLAUDE_CODE)
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

describe('write/edit tool_use messages never render the diff body', () => {
  const writeInput = { file_path: '/tmp/test.go', content: 'package main\n\nfunc main() {}\n' }

  it('does not render Write file content (no linked tool_result)', () => {
    const category = makeToolUseCategory('Write', writeInput)
    const { container } = render(() =>
      renderMessageContent(makeToolUseMessage('Write', writeInput), undefined, category, AgentProvider.CLAUDE_CODE),
    )
    // The diff body lives on the tool_result; tool_use only shows the header.
    expect(container.textContent).not.toContain('package main')
    // The header still surfaces the file path.
    expect(container.textContent).toContain('test.go')
  })

  it('does not render Write file content even when linked tool_result is "update" type', () => {
    const toolResultParsed = parseMessageContent(makeFakeMessage({
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', content: 'Updated successfully.' }] },
      tool_use_result: {
        type: 'update',
        filePath: '/tmp/test.go',
        structuredPatch: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-old', '+new'] }],
      },
    }))
    const context: RenderContext = { toolResultParsed }
    const category = makeToolUseCategory('Write', writeInput)
    const { container } = render(() =>
      renderMessageContent(makeToolUseMessage('Write', writeInput), context, category, AgentProvider.CLAUDE_CODE),
    )
    // Same outcome as the no-linked-result case: never renders content.
    expect(container.textContent).not.toContain('package main')
  })

  it('does not render Edit old/new strings on the tool_use side', () => {
    const editInput = {
      file_path: '/tmp/test.go',
      old_string: 'beforeMarkerXYZ',
      new_string: 'afterMarkerXYZ',
    }
    const category = makeToolUseCategory('Edit', editInput)
    const { container } = render(() =>
      renderMessageContent(makeToolUseMessage('Edit', editInput), undefined, category, AgentProvider.CLAUDE_CODE),
    )
    expect(container.textContent).not.toContain('beforeMarkerXYZ')
    expect(container.textContent).not.toContain('afterMarkerXYZ')
    // Header still shows the file path.
    expect(container.textContent).toContain('test.go')
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
      const category: MessageCategory = { kind: 'tool_result' }
      const result = renderMessageContent(parsed, { spanType: 'Read', ...context }, category, AgentProvider.CLAUDE_CODE)
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
