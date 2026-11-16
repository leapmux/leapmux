import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { renderMessageContent } from '~/test-support/messageContentRenderer'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { providerRow } from '~/test-support/toolCallFixture'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { CLAUDE_TOOL_NAMES } from './toolNames'
import './plugin'
import '~/components/chat/providers'

// Claude's tool result is a `user` row carrying a `tool_result` block, and its tool
// NAME lives on the `tool_use` row it answers. With no request beside it, the row has
// only its content to show, and it must show that rather than a name it never read.
describe('claude tool rendering', () => {
  it('shows an unmatched completion by its content alone', async () => {
    const parsed = {
      type: 'user',
      message: {
        role: 'user',
        content: [
          { type: 'tool_result', tool_use_id: 'toolu_orphan', content: 'recovered output' },
        ],
      },
    }
    const plugin = providerFor(AgentProvider.CLAUDE_CODE)!
    const { container } = render(() => renderMessageContent(parsed, {
      premeasureMode: true,
      sources: testMessageSources({
        current: () => input(parsed),
        request: () => undefined,
      }),
    }, plugin?.transcript.classify(input(parsed)), AgentProvider.CLAUDE_CODE))
    await settleWorkerReplies()
    expect(container.textContent).toContain('recovered output')
    for (const invented of ['Run command', 'Read file', 'Search'])
      expect(container.textContent).not.toContain(invented)
  })
})

/**
 * The `Task*` family through the whole row pipeline.
 *
 * `TaskGet` carries NO input of its own -- the task arrives in the result. A request row
 * never reads its result, so it draws its header at once, and its result row draws the
 * task. The result rows of all three are visible rows.
 */
describe('claude Task rows', () => {
  const useRow = (name: string, toolInput: Record<string, unknown>) => ({
    type: 'assistant',
    message: {
      role: 'assistant',
      content: [
        {
          type: 'tool_use',
          id: 'toolu_1',
          name,
          input: toolInput,
        },
      ],
    },
  })

  const resultRow = (payload: Record<string, unknown>) => ({
    type: 'user',
    message: {
      role: 'user',
      content: [
        { type: 'tool_result', tool_use_id: 'toolu_1', content: 'ok' },
      ],
    },
    tool_use_result: payload,
  })

  // The reader sees the call at once: no row waits for its result.
  it('draws the request row of a TaskGet at once, with no task', () => {
    const row = providerRow(AgentProvider.CLAUDE_CODE, useRow(CLAUDE_TOOL_NAMES.TASK_GET, { task_id: 7 }), { role: 'request' })
    expect(row?.kind).toBe('tool')
    expect(row?.kind === 'tool' && row.call.kind).toBe('todo')
    expect(row?.kind === 'tool' && row.call.result).toBeUndefined()
  })

  it('draws the task on the result row', () => {
    const row = providerRow(AgentProvider.CLAUDE_CODE, resultRow({
      task: { id: 7, subject: 'Inspect sample', status: 'pending' },
    }), {
      role: 'result',
      spanType: CLAUDE_TOOL_NAMES.TASK_GET,
      request: input(useRow(CLAUDE_TOOL_NAMES.TASK_GET, { task_id: 7 })),
    })
    expect(row?.kind).toBe('tool')
    expect(row?.kind === 'tool' && row.call.kind).toBe('todo')
    expect(row?.kind === 'tool' && row.call.result).toEqual({
      items: [
        expect.objectContaining({ content: 'Inspect sample' }),
      ],
    })
  })

  // Both of these DO carry input, so each states what it asked while it runs.
  it.each([CLAUDE_TOOL_NAMES.TASK_CREATE, CLAUDE_TOOL_NAMES.TASK_UPDATE])('draws the request row of %s with no result', (name) => {
    const row = providerRow(AgentProvider.CLAUDE_CODE, useRow(name, { subject: 'Inspect sample' }), { role: 'request' })
    expect(row?.kind).toBe('tool')
    expect(row?.kind === 'tool' && row.call.result).toBeUndefined()
  })
})

/**
 * A sibling belongs to THIS call only when its tool-use id says so: one turn can carry
 * several parallel calls. A result row reads its request, and a request from another
 * call in the same turn must not reach this row.
 */
describe('claude Task rows and a sibling from another call', () => {
  it('ignores a request whose tool-use id identifies another call', () => {
    const stranger = {
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [
          {
            type: 'tool_use',
            id: 'toolu_other',
            name: CLAUDE_TOOL_NAMES.TASK_CREATE,
            input: { subject: 'Another call' },
          },
        ],
      },
    }
    const mine = {
      type: 'user',
      message: {
        role: 'user',
        content: [
          { type: 'tool_result', tool_use_id: 'toolu_mine', content: 'ok' },
        ],
      },
      tool_use_result: {
        task: { id: 7, subject: 'Inspect sample' },
      },
    }
    const row = providerRow(AgentProvider.CLAUDE_CODE, mine, {
      role: 'result',
      spanType: CLAUDE_TOOL_NAMES.TASK_CREATE,
      request: input(stranger),
    })
    expect(row?.kind === 'tool' && row.call.result).toEqual({
      items: [
        expect.objectContaining({ content: 'Inspect sample' }),
      ],
    })
    expect(JSON.stringify(row)).not.toContain('Another call')
  })
})

/**
 * The `ExitPlanMode` rows.
 *
 * The result row states the words "Plan approved" and the file the command line
 * interface wrote the plan to. The request row heads itself with the result row's call
 * once the result lands (see ToolMessage), so the header reads "Plan approved" there
 * too. The plan file is a fact of the result, which the result row draws.
 */
describe('claude ExitPlanMode rows', () => {
  const useRow = (name: string, toolInput: Record<string, unknown> = {}) => ({
    type: 'assistant',
    message: {
      role: 'assistant',
      content: [
        {
          type: 'tool_use',
          id: 'toolu_1',
          name,
          input: toolInput,
        },
      ],
    },
  })

  const resultRow = (payload: Record<string, unknown>, isError = false) => ({
    type: 'user',
    message: {
      role: 'user',
      content: [
        {
          type: 'tool_result',
          tool_use_id: 'toolu_1',
          content: isError ? 'Try a smaller scope.' : 'ok',
          is_error: isError,
        },
      ],
    },
    tool_use_result: payload,
  })

  /** The call of the RESULT row of an ExitPlanMode call. */
  function answeredCall(payload: Record<string, unknown>, isError = false) {
    const row = providerRow(AgentProvider.CLAUDE_CODE, resultRow(payload, isError), {
      role: 'result',
      spanType: CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE,
      request: input(useRow(CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE)),
    })
    return row?.kind === 'tool'
      ? row.call
      : null
  }

  it('heads an approved plan with words rather than the mode token', () => {
    const call = answeredCall({ filePath: '/repo/.plans/one.md' })
    expect(call?.title).toBe('Plan approved')
    expect(call?.kind === 'switch_mode' && call.request.mode).toBeUndefined()
  })

  it('states the plan file the interface wrote as a fact of the result', () => {
    const call = answeredCall({ filePath: '/repo/.plans/one.md' })
    expect(call?.resultMetadata).toEqual([
      { label: 'Plan file', value: '/repo/.plans/one.md' },
    ])
    expect(call?.metadata).toBeUndefined()
  })

  it('states no plan file when the result carries none', () => {
    expect(answeredCall({})?.resultMetadata).toBeUndefined()
  })

  // The request row reads only its own frame, so it states neither the answer nor the
  // file while the call runs.
  it('states no answer on the request row', () => {
    const row = providerRow(AgentProvider.CLAUDE_CODE, useRow(CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE), { role: 'request' })
    const call = row?.kind === 'tool'
      ? row.call
      : null
    expect(call?.title).toBe('Exit plan mode')
    expect(call?.result).toBeUndefined()
    expect(call?.resultMetadata).toBeUndefined()
  })

  // The refusal is an ANSWER the agent asked for, and the kind words that outcome
  // "Sent feedback" from the status rather than from a title.
  it('words a refused plan as declined and keeps the feedback', () => {
    const call = answeredCall({}, true)
    expect(call?.status).toBe('declined')
    expect(call?.result).toEqual({ text: 'Try a smaller scope.', format: 'plain' })
    expect(call?.resultMetadata).toBeUndefined()
  })

  // A worktree move still states its mode, which is what tells entering one from
  // leaving one: both used to state `worktree`, so the two rows drew one title.
  it.each([
    [CLAUDE_TOOL_NAMES.ENTER_WORKTREE, 'worktree'],
    [CLAUDE_TOOL_NAMES.EXIT_WORKTREE, 'default'],
  ])('keeps the mode and the target of a %s call', (name, mode) => {
    const row = providerRow(AgentProvider.CLAUDE_CODE, useRow(name, { name: 'feature-a' }), { role: 'request' })
    const call = row?.kind === 'tool'
      ? row.call
      : null
    expect(call?.kind === 'switch_mode' && call.request).toEqual({ mode, target: 'feature-a' })
  })
})
