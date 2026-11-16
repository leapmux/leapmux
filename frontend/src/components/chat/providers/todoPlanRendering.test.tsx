import type { MessageCategory } from '../messageClassifier'
import type { MessageContentRenderContext } from '../messageContentRenderer'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { toolOutcomeLabel } from '../results/toolOutcomeLabel'
import { resolveMessageForRendering } from './registry'
import './claude/plugin'
import './codex/plugin'
import './opencode/plugin'
import '~/components/chat/providers'

const { renderMessageContent, renderPendingMessageContent } = await import('../messageContentRenderer')
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
      content: [
        {
          type: 'tool_use',
          id: `toolu_${name}_1`,
          name,
          input,
        },
      ],
    },
  }
}

function renderClaudeToolUse(name: string, input: Record<string, unknown>, context?: MessageContentRenderContext) {
  const parsed = makeClaudeToolUseMessage(name, input)
  const category = { kind: 'tool_use' } as MessageCategory
  const result = renderMessageContent(parsed, context, category, AgentProvider.CLAUDE_CODE)
  return render(() => result)
}

/** The PENDING row of a running Claude call: the preview of what it asked. */
function renderClaudePending(name: string, input: Record<string, unknown>) {
  const parsed = makeClaudeToolUseMessage(name, input)
  const category = { kind: 'tool_use' } as MessageCategory
  return render(() => renderPendingMessageContent(parsed, undefined, category, AgentProvider.CLAUDE_CODE))
}

/**
 * The RESULT row of a Claude call, with its request beside it. `metadata` is the message
 * metadata the worker stores on the result, such as the TaskUpdate snapshot.
 */
function renderClaudeResult(name: string, input: Record<string, unknown>, toolUseResult: Record<string, unknown>, metadata?: Record<string, unknown>) {
  const request = makeClaudeToolUseMessage(name, input)
  const parentObject = {
    type: 'user',
    message: {
      role: 'user',
      content: [
        { type: 'tool_result', tool_use_id: `toolu_${name}_1`, content: 'ok' },
      ],
    },
    tool_use_result: toolUseResult,
  }
  const resolved = resolveMessageForRendering({
    rawText: '',
    topLevel: parentObject,
    parentObject,
    wrapper: null,
    messageMetadata: metadata ?? {},
  }, AgentProvider.CLAUDE_CODE)
  const requestResolved = resolveMessageForRendering({
    rawText: '',
    topLevel: request,
    parentObject: request,
    wrapper: null,
  }, AgentProvider.CLAUDE_CODE)
  const context: MessageContentRenderContext = {
    spanType: name,
    sources: testMessageSources({
      current: () => resolved,
      request: () => requestResolved,
      role: () => 'result',
      visibleRows: () => ({ request: true }),
    }),
  }
  const category = { kind: 'tool_result' } as MessageCategory
  return render(() => renderMessageContent(parentObject, context, category, AgentProvider.CLAUDE_CODE))
}

function taskUpdateContext(
  input: Record<string, unknown>,
  snapshot: Record<string, unknown> | undefined,
): MessageContentRenderContext {
  const parentObject = makeClaudeToolUseMessage('TaskUpdate', input)
  const resolved = resolveMessageForRendering({
    rawText: '',
    topLevel: parentObject,
    parentObject,
    wrapper: null,
    messageMetadata: snapshot === undefined
      ? {}
      : { [MESSAGE_METADATA_FIELD.TodoSnapshot]: snapshot },
  }, AgentProvider.CLAUDE_CODE)
  return {
    sources: testMessageSources({
      current: () => resolved,
    }),
  }
}

/**
 * Render one Codex item through the CLASSIFIER, not through a hardcoded category.
 *
 * A plan item is the reason: `classify` answers `assistant_plan` for one and
 * `tool_use` for every other item, and a test that stated the category itself would
 * pass while the two layers disagreed -- which is exactly the defect this file now
 * covers end to end.
 */
function renderCodexItem(item: Record<string, unknown>, context?: MessageContentRenderContext) {
  const parsed = { item, threadId: 't1', turnId: 'r1' }
  const category = classifyMessage({
    ...resolveMessageForRendering({
      rawText: '',
      topLevel: parsed,
      parentObject: parsed,
      wrapper: null,
    }, AgentProvider.CODEX),
    agentProvider: AgentProvider.CODEX,
  })
  const result = renderMessageContent(parsed, context, category, AgentProvider.CODEX)
  return render(() => result)
}

function renderCodexTurnPlan(parsed: Record<string, unknown>, context?: MessageContentRenderContext) {
  const category: MessageCategory = { kind: 'tool_use' }
  const result = renderMessageContent(parsed, context, category, AgentProvider.CODEX)
  return render(() => result)
}

function renderOpenCodePlan(toolUse: Record<string, unknown>, context?: MessageContentRenderContext) {
  const category: MessageCategory = { kind: 'tool_use' }
  const result = renderMessageContent(toolUse, context, category, AgentProvider.OPENCODE)
  return render(() => result)
}

// ---------------------------------------------------------------------------
// Claude Code TodoWrite
// ---------------------------------------------------------------------------

describe('claude TodoWrite renders via the shared TodoListBody', () => {
  const todos = [
    {
      rowKey: 'first task',
      content: 'first task',
      status: 'pending',
      activeForm: 'doing first',
    },
    {
      rowKey: 'second task',
      content: 'second task',
      status: 'in_progress',
      activeForm: 'doing second',
    },
  ]

  // The request row heads the call with its size; the pending row below it states the
  // list the call carries while it runs.
  it('renders the pluralized title on the request row and the list on the pending row', () => {
    const request = renderClaudeToolUse('TodoWrite', { todos })
    expect(request.container.textContent ?? '').toContain('2 tasks')
    expect(request.container.textContent ?? '').not.toContain('first task')
    const pending = renderClaudePending('TodoWrite', { todos })
    const text = pending.container.textContent ?? ''
    expect(text).toContain('first task')
    // in_progress entries render their activeForm.
    expect(text).toContain('doing second')
  })

  // Only the result can say that the call cleared the list: the result row states it.
  it('renders the cleared placeholder for an empty list on the result row', () => {
    const { container } = renderClaudeResult('TodoWrite', { todos: [] }, { newTodos: [] })
    expect(container.textContent ?? '').toContain('To-do list cleared')
    expect(renderClaudePending('TodoWrite', { todos: [] }).container.textContent ?? '').toBe('')
  })
})

// ---------------------------------------------------------------------------
// Claude Code TaskCreate / TaskUpdate / TaskList / TaskGet (2.1.142+)
// ---------------------------------------------------------------------------

describe('claude TaskCreate renders a single-row card', () => {
  it('renders the subject and the description on the pending row while the call runs', async () => {
    const { container } = renderClaudePending('TaskCreate', {
      subject: 'Add proto messages',
      description: 'Edit proto/agent.proto',
      activeForm: 'Adding proto',
    })
    await settleWorkerReplies()
    const text = container.textContent ?? ''
    expect(text).toContain('Add proto messages')
    expect(text).toContain('Edit proto/agent.proto')
    expect(container.querySelector('[data-task-checkbox="pending"]')).toBeTruthy()
  })

  it('renders without a summary line when no description is provided', () => {
    const { container } = renderClaudePending('TaskCreate', { subject: 'Bare task' })
    expect(container.textContent ?? '').toContain('Bare task')
    expect(container.querySelector('[data-task-checkbox="pending"]')).toBeTruthy()
  })

  it('falls back to "New task" when the input has no subject', () => {
    const { container } = renderClaudePending('TaskCreate', {})
    expect(container.textContent ?? '').toContain('New task')
  })

  it('renders the created task on the result row', async () => {
    const { container } = renderClaudeResult('TaskCreate', { subject: 'Add proto messages' }, {
      task: { id: '3', subject: 'Add proto messages' },
    })
    await settleWorkerReplies()
    expect(container.textContent ?? '').toContain('Add proto messages')
    expect(container.querySelector('[data-task-checkbox="pending"]')).toBeTruthy()
  })
})

describe('claude TaskUpdate renders a single-row card', () => {
  // The worker stores the post-update snapshot on the RESULT message, so the result row
  // states a missing one, and the request row asks for none.
  it('renders a diagnostic on the result row and warns once when the required snapshot is absent', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { container } = renderClaudeResult('TaskUpdate', { taskId: '1', status: 'in_progress' }, { success: true, taskId: '1' })
    expect(container.textContent ?? '').toContain('TaskUpdate metadata is missing todo_snapshot')
    renderClaudeResult('TaskUpdate', { taskId: '1', status: 'in_progress' }, { success: true, taskId: '1' })
    expect(warn.mock.calls.filter(call => call[0] === '[claudeTaskUpdate]')).toHaveLength(1)
    warn.mockRestore()
  })

  // The Worker stores a snapshot only for an update that succeeded and names its task. A
  // failed update states its own error, and it misses no snapshot.
  it('states the error of a failed update, and no snapshot diagnostic', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { container } = renderClaudeResult('TaskUpdate', { taskId: '3', status: 'in_progress' }, { success: false, taskId: '3', error: 'Task #3 not found' })
    const text = container.textContent ?? ''
    expect(text).toContain('Task #3 not found')
    expect(text).toContain(toolOutcomeLabel('failed'))
    expect(text).not.toContain('todo_snapshot')
    expect(warn.mock.calls.filter(call => call[0] === '[claudeTaskUpdate]')).toHaveLength(0)
    warn.mockRestore()
  })

  // A failure that states no words of its own falls back to the text of the tool result.
  it('states the result text of a failed update that states no error', () => {
    const { container } = renderClaudeResult('TaskUpdate', { taskId: '4', status: 'in_progress' }, { success: false, taskId: '4' })
    const text = container.textContent ?? ''
    expect(text).toContain('ok')
    expect(text).toContain(toolOutcomeLabel('failed'))
    expect(text).not.toContain('todo_snapshot')
  })

  it('states no diagnostic on the request row, which never reads the snapshot', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { container } = renderClaudeToolUse('TaskUpdate', { taskId: '1', status: 'in_progress' })
    expect(container.textContent ?? '').not.toContain('todo_snapshot')
    expect(warn.mock.calls.filter(call => call[0] === '[claudeTaskUpdate]')).toHaveLength(0)
    warn.mockRestore()
  })

  it('renders the persisted snapshot of a status-only patch on the result row', async () => {
    const { container } = renderClaudeResult('TaskUpdate', { taskId: '42', status: 'completed' }, { success: true, taskId: '42' }, {
      [MESSAGE_METADATA_FIELD.TodoSnapshot]: {
        id: '42',
        content: 'Persisted subject',
        status: 'TODO_STATUS_COMPLETED',
        activeForm: '',
        description: 'Persisted description',
      },
    })
    await settleWorkerReplies()
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
    const { container } = renderClaudeResult('TaskUpdate', input, { success: true, taskId: '7' }, {
      [MESSAGE_METADATA_FIELD.TodoSnapshot]: {
        id: '7',
        content: 'Persisted subject',
        status: 'TODO_STATUS_IN_PROGRESS',
        activeForm: 'Persisted active form',
      },
    })
    const text = container.textContent ?? ''
    expect(text).toContain('Persisted active form')
    expect('todo' in (taskUpdateContext(input, undefined).sources ?? {})).toBe(false)
  })
})

describe('claude TaskGet renders a single-row card on its result row', () => {
  it('renders subject + description from tool_use_result.task', async () => {
    const { container } = renderClaudeResult('TaskGet', {}, {
      task: {
        id: '9',
        subject: 'Get me',
        description: 'A long task',
        status: 'completed',
      },
    })
    await settleWorkerReplies()
    const text = container.textContent ?? ''
    expect(text).toContain('Get me')
    expect(text).toContain('A long task')
    expect(container.querySelector('[data-task-checkbox="completed"]')).toBeTruthy()
  })

  it('renders the deleted checkbox when the looked-up task is a tombstone', () => {
    const { container } = renderClaudeResult('TaskGet', {}, {
      task: { id: '11', subject: 'Already gone', status: 'deleted' },
    })
    expect(container.textContent ?? '').toContain('Already gone')
    expect(container.querySelector('[data-task-checkbox="deleted"]')).toBeTruthy()
  })

  // `TaskGet` carries no input of its own. Its request row draws its header at once, and
  // previews nothing: no row says "To-do list cleared" about a list the call never touched.
  it('draws its header at once and previews nothing before its result lands', () => {
    const { container } = renderClaudeToolUse('TaskGet', {})
    expect(container.textContent ?? '').toContain('Task')
    expect(container.textContent ?? '').not.toContain('cleared')
    expect(renderClaudePending('TaskGet', {}).container.textContent ?? '').toBe('')
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
            content: [
              {
                type: 'tool_use',
                id: 'toolu_x',
                name: 'TaskList',
                input: {},
              },
            ],
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
  it('renders the markdown body and the "Proposed Plan" header', async () => {
    const { container } = renderCodexItem({
      type: 'plan',
      text: '# Codex proposed plan body\n\n- step one',
    })
    await settleWorkerReplies()
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

describe('claude classifies Task* tool_result messages', () => {
  function classifyResult(tool: string) {
    return classifyMessage({
      ...resolveMessageForRendering({
        rawText: '',
        topLevel: null,
        parentObject: {
          type: 'user',
          message: {
            content: [
              { type: 'tool_result', tool_use_id: 'x', content: '' },
            ],
          },
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
  }

  // A request row never reads its result, so the result row of these three is the one
  // that states the task the answer carries.
  it.each(['TaskCreate', 'TaskUpdate', 'TaskGet'])('draws the %s tool_result as a row of its own', (tool) => {
    expect(classifyResult(tool).kind).toBe('tool_result')
  })

  it('hides the TaskList tool_result: the to-do sidebar already shows the list', () => {
    expect(classifyResult('TaskList').kind).toBe('hidden')
  })
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
