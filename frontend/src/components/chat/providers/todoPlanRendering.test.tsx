import type { MessageCategory } from '../messageClassifier'
import type { RenderContext } from '../messageRenderers'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { resolveMessageForRendering } from './registry'
import './claude/plugin'
import './codex/plugin'
import './opencode/plugin'
import './testMocks'

vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: async (_lang: string, code: string) => code.split('\n').map(() => []),
}))

vi.mock('~/lib/tokenCache', () => ({
  getCachedTokens: () => null,
}))

const { renderMessageContent } = await import('../messageContentRenderer')
const { classifyMessage } = await import('../messageClassifier')

interface ToolUsePayload extends Record<string, unknown> {
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

function renderClaudeToolUse(name: string, input: Record<string, unknown>, context?: RenderContext) {
  const parsed = makeClaudeToolUseMessage(name, input)
  const category = { kind: 'tool_use' } as MessageCategory
  const result = renderMessageContent(parsed, context, category, AgentProvider.CLAUDE_CODE)
  return render(() => result)
}

function taskUpdateContext(
  input: Record<string, unknown>,
  snapshot: Record<string, unknown> | undefined,
): RenderContext {
  const parentObject = makeClaudeToolUseMessage('TaskUpdate', input)
  const resolved = resolveMessageForRendering({
    rawText: '',
    topLevel: parentObject,
    parentObject,
    wrapper: null,
    messageMetadata: snapshot === undefined ? {} : { [MESSAGE_METADATA_FIELD.TodoSnapshot]: snapshot },
  }, AgentProvider.CLAUDE_CODE)
  return { sources: testMessageSources({ current: () => resolved }) }
}

/**
 * Render one Codex item through the CLASSIFIER, not through a hardcoded category.
 *
 * A plan item is the reason: `classify` answers `assistant_plan` for one and
 * `tool_use` for every other item, and a test that stated the category itself would
 * pass while the two layers disagreed -- which is exactly the defect this file now
 * covers end to end.
 */
function renderCodexItem(item: Record<string, unknown>, context?: RenderContext) {
  const parsed = { item, threadId: 't1', turnId: 'r1' }
  const category = classifyMessage({
    ...resolveMessageForRendering({ rawText: '', topLevel: parsed, parentObject: parsed, wrapper: null }, AgentProvider.CODEX),
    agentProvider: AgentProvider.CODEX,
  })
  const result = renderMessageContent(parsed, context, category, AgentProvider.CODEX)
  return render(() => result)
}

function renderCodexTurnPlan(parsed: Record<string, unknown>, context?: RenderContext) {
  const category: MessageCategory = { kind: 'tool_use' }
  const result = renderMessageContent(parsed, context, category, AgentProvider.CODEX)
  return render(() => result)
}

function renderOpenCodePlan(toolUse: Record<string, unknown>, context?: RenderContext) {
  const category: MessageCategory = { kind: 'tool_use' }
  const result = renderMessageContent(toolUse, context, category, AgentProvider.OPENCODE)
  return render(() => result)
}

// ---------------------------------------------------------------------------
// Claude Code TodoWrite
// ---------------------------------------------------------------------------

describe('claude TodoWrite renders via the shared TodoListBody', () => {
  it('renders the pluralized title and todo content', () => {
    const { container } = renderClaudeToolUse('TodoWrite', {
      todos: [
        { rowKey: 'first task', content: 'first task', status: 'pending', activeForm: 'doing first' },
        { rowKey: 'second task', content: 'second task', status: 'in_progress', activeForm: 'doing second' },
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
// Claude Code TaskCreate / TaskUpdate / TaskList / TaskGet (2.1.142+)
// ---------------------------------------------------------------------------

describe('claude TaskCreate renders a single-row card', () => {
  it('renders the subject as the title and the description as the summary', () => {
    const { container } = renderClaudeToolUse('TaskCreate', {
      subject: 'Add proto messages',
      description: 'Edit proto/agent.proto',
      activeForm: 'Adding proto',
    })
    const text = container.textContent ?? ''
    expect(text).toContain('Add proto messages')
    expect(text).toContain('Edit proto/agent.proto')
    expect(container.querySelector('[data-task-checkbox="pending"]')).toBeTruthy()
  })

  it('renders without a summary line when no description is provided', () => {
    const { container } = renderClaudeToolUse('TaskCreate', { subject: 'Bare task' })
    expect(container.textContent ?? '').toContain('Bare task')
    expect(container.querySelector('[data-task-checkbox="pending"]')).toBeTruthy()
  })

  it('falls back to "New task" when the input has no subject', () => {
    const { container } = renderClaudeToolUse('TaskCreate', {})
    expect(container.textContent ?? '').toContain('New task')
  })
})

describe('claude TaskUpdate renders a single-row card', () => {
  it('renders a diagnostic and warns once when the required snapshot is absent', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { container } = renderClaudeToolUse('TaskUpdate', {
      taskId: '1',
      status: 'in_progress',
    })
    const text = container.textContent ?? ''
    expect(text).toContain('TaskUpdate metadata is missing todo_snapshot')
    renderClaudeToolUse('TaskUpdate', { taskId: '1', status: 'in_progress' })
    expect(warn.mock.calls.filter(call => call[0] === '[claudeTaskUpdate]')).toHaveLength(1)
    warn.mockRestore()
  })

  it('renders the persisted snapshot on a status-only patch', () => {
    const input = {
      taskId: '42',
      status: 'completed',
    }
    const { container } = renderClaudeToolUse('TaskUpdate', input, taskUpdateContext(input, {
      id: '42',
      content: 'Persisted subject',
      status: 'TODO_STATUS_COMPLETED',
      activeForm: '',
      description: 'Persisted description',
    }))
    const text = container.textContent ?? ''
    expect(text).toContain('Persisted subject')
    expect(text).toContain('Persisted description')
    expect(container.querySelector('[data-task-checkbox="completed"]')).toBeTruthy()
  })

  it('does not read the live store after the snapshot is persisted', () => {
    const input = {
      taskId: '7',
      status: 'in_progress',
    }
    const { container } = renderClaudeToolUse('TaskUpdate', input, taskUpdateContext(input, {
      id: '7',
      content: 'Persisted subject',
      status: 'TODO_STATUS_IN_PROGRESS',
      activeForm: 'Persisted active form',
    }))
    const text = container.textContent ?? ''
    expect(text).toContain('Persisted active form')
    expect('todo' in (taskUpdateContext(input, undefined).sources ?? {})).toBe(false)
  })
})

describe('claude TaskGet renders a single-row card from the paired tool_result', () => {
  /**
   * The result side of a `TaskGet` span, in the shape Claude sends it.
   *
   * The `tool_result` block and its `tool_use_id` are not decoration: a sibling
   * belongs to THIS call only when its id says so, and the row reads the paired
   * payload through that test. A fixture that carried `tool_use_result` alone stated
   * a side no envelope of Claude's ever has.
   */
  function taskResult(name: string, task: Record<string, unknown>): Record<string, unknown> {
    return {
      parentObject: {
        type: 'user',
        message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: `toolu_${name}_1`, content: 'ok' }] },
        tool_use_result: { task },
      },
    }
  }

  it('renders subject + description from tool_use_result.task', () => {
    const result = taskResult('TaskGet', { id: '9', subject: 'Get me', description: 'A long task', status: 'completed' })
    const { container } = renderClaudeToolUse('TaskGet', {}, { sources: testMessageSources({ result: () => (result as never) }) })
    const text = container.textContent ?? ''
    expect(text).toContain('Get me')
    expect(text).toContain('A long task')
    expect(container.querySelector('[data-task-checkbox="completed"]')).toBeTruthy()
  })

  it('renders the deleted checkbox when the looked-up task is a tombstone', () => {
    const result = taskResult('TaskGet', { id: '11', subject: 'Already gone', status: 'deleted' })
    const { container } = renderClaudeToolUse('TaskGet', {}, { sources: testMessageSources({ result: () => (result as never) }) })
    expect(container.textContent ?? '').toContain('Already gone')
    expect(container.querySelector('[data-task-checkbox="deleted"]')).toBeTruthy()
  })

  // `TaskGet` carries no input of its own, so an unresolved one has nothing to draw.
  // Its empty checklist drew the to-do body's "To-do list cleared" under a bare
  // "Task" header -- a sentence about a list the call never touched.
  it('draws nothing at all before its result lands', () => {
    const { container } = renderClaudeToolUse('TaskGet', {})
    expect(container.textContent ?? '').toBe('')
  })
})

describe('claude classifies TaskList tool_use as hidden', () => {
  it('hides the TaskList tool_use so the chat surface stays quiet', () => {
    const category = classifyMessage({
      ...resolveMessageForRendering({
        rawText: '',
        topLevel: null,
        parentObject: {
          type: 'assistant',
          message: {
            content: [{ type: 'tool_use', id: 'toolu_x', name: 'TaskList', input: {} }],
          },
        },
        wrapper: null,
      }, AgentProvider.CLAUDE_CODE),
      agentProvider: AgentProvider.CLAUDE_CODE,
      spanId: 'span',
      spanType: 'TaskList',
      parentSpanId: '',
      seq: 0n,
      createdAt: '',
    })
    expect(category.kind).toBe('hidden')
  })
})

// ---------------------------------------------------------------------------
// Codex turn/plan/updated
// ---------------------------------------------------------------------------

describe('codex turn/plan/updated renders via the shared TodoListBody', () => {
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

describe('claude classifies Task* tool_result messages as hidden', () => {
  const taskTools = ['TaskCreate', 'TaskUpdate', 'TaskGet', 'TaskList'] as const
  for (const tool of taskTools) {
    it(`hides the ${tool} tool_result`, () => {
      const category = classifyMessage({
        ...resolveMessageForRendering({
          rawText: '',
          topLevel: null,
          parentObject: {
            type: 'user',
            message: { content: [{ type: 'tool_result', tool_use_id: 'x', content: '' }] },
          },
          wrapper: null,
        }, AgentProvider.CLAUDE_CODE),
        agentProvider: AgentProvider.CLAUDE_CODE,
        spanId: 'span',
        spanType: tool,
        parentSpanId: '',
        seq: 0n,
        createdAt: '',
      })
      expect(category.kind).toBe('hidden')
    })
  }
})

describe('opencode plan renders via the shared TodoListBody', () => {
  it('renders the entries as todos', () => {
    const { container } = renderOpenCodePlan({
      sessionUpdate: 'plan',
      entries: [
        { rowKey: 'opencode entry one', content: 'opencode entry one', status: 'pending' },
        { rowKey: 'opencode entry two', content: 'opencode entry two', status: 'completed' },
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
