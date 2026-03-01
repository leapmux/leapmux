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

  it('shows "Running" when thread children exist but no status', () => {
    const text = renderToolUseText('Agent', { description: 'Analyze code' }, {
      threadChildCount: 1,
    })
    expect(text).toContain('Analyze code')
    expect(text).toContain('Running')
  })

  it('shows "Complete" when childToolResultStatus is "completed"', () => {
    const text = renderToolUseText('Agent', { description: 'Analyze code' }, {
      threadChildCount: 2,
      childToolResultStatus: 'completed',
    })
    expect(text).toContain('Analyze code')
    expect(text).toContain('Complete')
  })

  it('shows "Failed" when childToolResultStatus is "failed"', () => {
    const text = renderToolUseText('Task', { description: 'Build project' }, {
      threadChildCount: 2,
      childToolResultStatus: 'failed',
    })
    expect(text).toContain('Build project')
    expect(text).toContain('Failed')
  })

  it('shows no status when no thread children', () => {
    const text = renderToolUseText('Agent', { description: 'Search' })
    expect(text).toBe('Search')
    expect(text).not.toContain('Running')
    expect(text).not.toContain('Complete')
  })

  it('renders description + subagent_type in title, status in summary', () => {
    const text = renderToolUseText('Agent', { description: 'Fix bug', subagent_type: 'code' }, {
      threadChildCount: 2,
      childToolResultStatus: 'completed',
    })
    expect(text).toContain('Fix bug (code)')
    expect(text).toContain('Complete')
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

  it('shows stats summary when stats are provided', () => {
    const text = renderToolUseText('Agent', { description: 'Search' }, {
      threadChildCount: 1,
      childToolResultStatus: 'completed',
      childTotalDurationMs: 30000,
      childTotalTokens: 500,
      childTotalToolUseCount: 3,
    })
    expect(text).toContain('30s')
    expect(text).toContain('500 tokens')
    expect(text).toContain('3 tool uses')
  })
})
