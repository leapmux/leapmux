import type { MessageCategory } from '../messageClassification'
import type { RenderContext } from '../messageRenderers'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import './claude'
import './codex'
import './opencode'
import './testMocks'

vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: async (_lang: string, code: string) => code.split('\n').map(() => []),
}))

vi.mock('~/lib/tokenCache', () => ({
  getCachedTokens: () => null,
}))

const { renderMessageContent } = await import('../messageRenderers')

interface ToolUsePayload {
  type: string
  message: { role: string, content: Array<Record<string, unknown>> }
}

function makeClaudeToolUseMessage(name: string, input: Record<string, unknown>): ToolUsePayload {
  return {
    type: 'assistant',
    message: {
      role: 'assistant',
      content: [{ type: 'tool_use', id: `toolu_${name}_1`, name, input }],
    },
  }
}

function makeClaudeToolUseCategory(name: string, input: Record<string, unknown>): MessageCategory {
  const toolUse = { type: 'tool_use' as const, id: `toolu_${name}_1`, name, input }
  return { kind: 'tool_use', toolName: name, toolUse, content: [toolUse] }
}

function renderClaudeToolUse(name: string, input: Record<string, unknown>, context?: RenderContext) {
  const parsed = makeClaudeToolUseMessage(name, input)
  const category = makeClaudeToolUseCategory(name, input)
  const result = renderMessageContent(parsed, MessageRole.ASSISTANT, context, category, AgentProvider.CLAUDE_CODE)
  return render(() => result)
}

function renderCodexItem(item: Record<string, unknown>, context?: RenderContext) {
  const parsed = { item, threadId: 't1', turnId: 'r1' }
  const toolName = String(item.type ?? 'codex')
  const category: MessageCategory = {
    kind: 'tool_use',
    toolName,
    toolUse: parsed,
    content: [],
  }
  const result = renderMessageContent(parsed, MessageRole.ASSISTANT, context, category, AgentProvider.CODEX)
  return render(() => result)
}

function renderCodexTurnPlan(parsed: Record<string, unknown>, context?: RenderContext) {
  const category: MessageCategory = {
    kind: 'tool_use',
    toolName: 'turnPlan',
    toolUse: parsed,
    content: [],
  }
  const result = renderMessageContent(parsed, MessageRole.LEAPMUX, context, category, AgentProvider.CODEX)
  return render(() => result)
}

function renderOpenCodePlan(toolUse: Record<string, unknown>, context?: RenderContext) {
  const category: MessageCategory = {
    kind: 'tool_use',
    toolName: 'plan',
    toolUse,
    content: [],
  }
  const result = renderMessageContent(toolUse, MessageRole.ASSISTANT, context, category, AgentProvider.OPENCODE)
  return render(() => result)
}

// ---------------------------------------------------------------------------
// Claude Code TodoWrite
// ---------------------------------------------------------------------------

describe('claude TodoWrite renders via shared TodoListMessage', () => {
  it('renders the pluralized title and todo content', () => {
    const { container } = renderClaudeToolUse('TodoWrite', {
      todos: [
        { content: 'first task', status: 'pending', activeForm: 'doing first' },
        { content: 'second task', status: 'in_progress', activeForm: 'doing second' },
      ],
    })
    const text = container.textContent ?? ''
    expect(text).toContain('2 tasks')
    expect(text).toContain('first task')
    // in_progress entries render their activeForm.
    expect(text).toContain('doing second')
  })

  it('renders the cleared placeholder for an empty todos list', () => {
    const { container } = renderClaudeToolUse('TodoWrite', { todos: [] })
    const text = container.textContent ?? ''
    expect(text).toContain('To-do list cleared')
  })
})

// ---------------------------------------------------------------------------
// Codex turn/plan/updated
// ---------------------------------------------------------------------------

describe('codex turn/plan/updated renders via shared TodoListMessage', () => {
  it('renders the pluralized title with explanation', () => {
    const parsed = {
      method: 'turn/plan/updated',
      params: {
        plan: [
          { step: 'first codex step', status: 'pending' },
          { step: 'second codex step', status: 'inProgress' },
        ],
        explanation: 'fix login bug',
      },
    }
    const { container } = renderCodexTurnPlan(parsed)
    const text = container.textContent ?? ''
    expect(text).toContain('2 tasks - fix login bug')
    expect(text).toContain('first codex step')
    expect(text).toContain('second codex step')
  })

  it('renders the cleared placeholder for an empty plan', () => {
    const parsed = {
      method: 'turn/plan/updated',
      params: { plan: [] },
    }
    const { container } = renderCodexTurnPlan(parsed)
    const text = container.textContent ?? ''
    expect(text).toContain('To-do list cleared')
  })
})

// ---------------------------------------------------------------------------
// Codex proposed-plan markdown
// ---------------------------------------------------------------------------

describe('codex plan item renders proposed plan markdown', () => {
  it('renders the markdown body and the "Proposed Plan" header', () => {
    const { container } = renderCodexItem({
      type: 'plan',
      text: '# Codex proposed plan body\n\n- step one',
    })
    const text = container.textContent ?? ''
    expect(text).toContain('Proposed Plan')
    expect(text).toContain('Codex proposed plan body')
  })

  it('does not render the proposed-plan body when the plan item has no text', () => {
    const { container } = renderCodexItem({ type: 'plan', text: '' })
    const text = container.textContent ?? ''
    expect(text).not.toContain('Proposed Plan')
  })
})

// ---------------------------------------------------------------------------
// OpenCode plan
// ---------------------------------------------------------------------------

describe('opencode plan renders via shared TodoListMessage', () => {
  it('renders the entries as todos', () => {
    const { container } = renderOpenCodePlan({
      sessionUpdate: 'plan',
      entries: [
        { content: 'opencode entry one', status: 'pending' },
        { content: 'opencode entry two', status: 'completed' },
      ],
    })
    const text = container.textContent ?? ''
    expect(text).toContain('opencode entry one')
    expect(text).toContain('opencode entry two')
  })

  it('renders the cleared placeholder for an empty entries list', () => {
    const { container } = renderOpenCodePlan({
      sessionUpdate: 'plan',
      entries: [],
    })
    const text = container.textContent ?? ''
    expect(text).toContain('To-do list cleared')
  })
})
