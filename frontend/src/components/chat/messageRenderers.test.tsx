import type { RenderContext } from './messageRenderers'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { renderMessageContent } from './messageRenderers'

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

describe('user content renderer', () => {
  it('renders Codex turn/interrupt payload as a human-friendly message', () => {
    const parsed = {
      content: '{"jsonrpc":"2.0","id":1001,"method":"turn/interrupt","params":{"threadId":"thread-1","turnId":"turn-1"}}',
    }
    const result = renderMessageContent(parsed, 1 /* USER */)
    const { container } = render(() => result)
    expect(container.textContent?.trim()).toBe('[Request interrupted by user]')
  })

  it('renders Codex turn/interrupt user message content as a human-friendly message', () => {
    const parsed = {
      type: 'user',
      message: {
        content: '{"jsonrpc":"2.0","id":1001,"method":"turn/interrupt","params":{"threadId":"thread-1","turnId":"turn-1"}}',
      },
    }
    const result = renderMessageContent(parsed, 1 /* USER */)
    const { container } = render(() => result)
    expect(container.textContent?.trim()).toBe('[Request interrupted by user]')
  })
})
