import type { MessageCategory } from '../messageClassification'
import type { RenderContext } from '../messageRenderers'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageSources } from '~/test-support/messageRenderSources'
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

const { renderMessageContent } = await import('../rowRenderers')
const { classifyMessage } = await import('../messageClassification')

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

function renderClaudeToolUse(name: string, input: Record<string, unknown>, context?: RenderContext) {
  const parsed = makeClaudeToolUseMessage(name, input)
  const category = { kind: 'tool_use' } as MessageCategory
  const result = renderMessageContent(parsed, context, category, AgentProvider.CLAUDE_CODE)
  return render(() => result)
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
    rawText: '',
    topLevel: parsed,
    parentObject: parsed,
    wrapper: null,
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
  it('falls back to "Task #ID" when the patch has no subject and the store is empty', () => {
    const { container } = renderClaudeToolUse('TaskUpdate', {
      taskId: '1',
      status: 'in_progress',
    })
    const text = container.textContent ?? ''
    expect(text).toContain('Task #1')
    expect(container.querySelector('[data-task-checkbox="in_progress"]')).toBeTruthy()
  })

  it('resolves the subject from the live todos store on a status-only patch', () => {
    const { container } = renderClaudeToolUse('TaskUpdate', {
      taskId: '42',
      status: 'completed',
    }, { sources: testMessageSources({ todo: id => id === '42' ? { id: '42', rowKey: '42', content: 'Stored subject', status: 'pending', activeForm: '' } : undefined }) })
    const text = container.textContent ?? ''
    expect(text).toContain('Stored subject')
    expect(text).not.toContain('Task #42')
    expect(container.querySelector('[data-task-checkbox="completed"]')).toBeTruthy()
  })

  it('prefers the input subject over a (stale) store entry', () => {
    const { container } = renderClaudeToolUse('TaskUpdate', {
      taskId: '7',
      subject: 'Fresh subject from this patch',
      status: 'in_progress',
    }, { sources: testMessageSources({ todo: id => id === '7' ? { id: '7', rowKey: '7', content: 'Stale stored subject', status: 'pending', activeForm: '' } : undefined }) })
    const text = container.textContent ?? ''
    expect(text).toContain('Fresh subject from this patch')
    expect(text).not.toContain('Stale stored subject')
  })

  it('also resolves the subject from the store for the deleted card', () => {
    const { container } = renderClaudeToolUse('TaskUpdate', {
      taskId: '8',
      status: 'deleted',
    }, { sources: testMessageSources({ todo: id => id === '8' ? { id: '8', rowKey: '8', content: 'Will be removed', status: 'completed', activeForm: '' } : undefined }) })
    const text = container.textContent ?? ''
    expect(text).toContain('Will be removed')
    expect(container.querySelector('[data-task-checkbox="deleted"]')).toBeTruthy()
  })

  it('renders the deleted checkbox when status=deleted', () => {
    const { container } = renderClaudeToolUse('TaskUpdate', {
      taskId: '5',
      subject: 'tmp task',
      status: 'deleted',
    })
    const text = container.textContent ?? ''
    expect(text).toContain('tmp task')
    expect(container.querySelector('[data-task-checkbox="deleted"]')).toBeTruthy()
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
      rawText: '',
      topLevel: null,
      parentObject: {
        type: 'assistant',
        message: {
          content: [{ type: 'tool_use', id: 'toolu_x', name: 'TaskList', input: {} }],
        },
      },
      wrapper: null,
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
        rawText: '',
        topLevel: null,
        parentObject: {
          type: 'user',
          message: { content: [{ type: 'tool_result', tool_use_id: 'x', content: '' }] },
        },
        wrapper: null,
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
