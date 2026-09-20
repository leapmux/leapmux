import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { providerRow } from '~/test-support/toolCallFixture'
import { renderMessageContent } from '../../messageContentRenderer'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { CLAUDE_TOOL_NAMES } from './toolNames'
import './plugin'
import '../testMocks'

// Claude's tool result is a `user` row carrying a `tool_result` block, and its tool
// NAME lives on the `tool_use` row it answers. With no request beside it, the row has
// only its content to show, and it must show that rather than a name it never read.
describe('claude tool rendering', () => {
  it('shows an unmatched completion by its content alone', () => {
    const parsed = { type: 'user', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_orphan', content: 'recovered output' }] } }
    const plugin = providerFor(AgentProvider.CLAUDE_CODE)!
    const { container } = render(() => renderMessageContent(parsed, {
      premeasureMode: true,
      sources: testMessageSources({ current: () => input(parsed), request: () => undefined }),
    }, plugin?.transcript.classify(input(parsed)), AgentProvider.CLAUDE_CODE))
    expect(container.textContent).toContain('recovered output')
    for (const invented of ['Run command', 'Read file', 'Search'])
      expect(container.textContent).not.toContain(invented)
  })
})

/**
 * The `Task*` family through the whole row pipeline.
 *
 * `TaskGet` carries NO input of its own -- the task arrives in the result -- so an
 * unresolved one has nothing to draw. Its empty checklist drew the to-do body's own
 * "To-do list cleared" under a bare "Task" header, which is a sentence about a list
 * the call never touched.
 */
describe('claude Task rows before their result lands', () => {
  const useRow = (name: string, toolInput: Record<string, unknown>) => ({
    type: 'assistant',
    message: { role: 'assistant', content: [{ type: 'tool_use', id: 'toolu_1', name, input: toolInput }] },
  })

  const resultRow = (payload: Record<string, unknown>) => ({
    type: 'user',
    message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: 'ok' }] },
    tool_use_result: payload,
  })

  it('draws no row for an unresolved TaskGet', () => {
    const row = providerRow(AgentProvider.CLAUDE_CODE, useRow(CLAUDE_TOOL_NAMES.TASK_GET, { task_id: 7 }), { role: 'request' })
    expect(row).toEqual({ kind: 'hidden' })
  })

  it('draws the task once the result lands beside it', () => {
    const payload = useRow(CLAUDE_TOOL_NAMES.TASK_GET, { task_id: 7 })
    const result = resultRow({ task: { id: 7, subject: 'Inspect sample', status: 'pending' } })
    const row = providerRow(AgentProvider.CLAUDE_CODE, payload, { role: 'request', result: input(result) })
    expect(row?.kind).toBe('tool')
    expect(row?.kind === 'tool' && row.call.kind).toBe('todo')
  })

  // Both of these DO carry input, so each draws while it runs.
  it.each([CLAUDE_TOOL_NAMES.TASK_CREATE, CLAUDE_TOOL_NAMES.TASK_UPDATE])('still draws an unresolved %s', (name) => {
    const row = providerRow(AgentProvider.CLAUDE_CODE, useRow(name, { subject: 'Inspect sample' }), { role: 'request' })
    expect(row?.kind).toBe('tool')
  })
})

/**
 * A sibling belongs to THIS call only when its tool-use id says so: one turn can carry
 * several parallel calls. The paired payload the `Task*` rows read used to skip that
 * test, so a result from another call in the same turn reached this row's checklist.
 */
describe('claude Task rows and a sibling from another call', () => {
  it('ignores a result whose tool-use id names another call', () => {
    const payload = {
      type: 'assistant',
      message: { role: 'assistant', content: [{ type: 'tool_use', id: 'toolu_mine', name: CLAUDE_TOOL_NAMES.TASK_GET, input: { task_id: 7 } }] },
    }
    const stranger = {
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_other', content: 'ok' }] },
      tool_use_result: { task: { id: 99, subject: 'Another call', status: 'pending' } },
    }
    const row = providerRow(AgentProvider.CLAUDE_CODE, payload, { role: 'request', result: input(stranger) })
    expect(row).toEqual({ kind: 'hidden' })
  })
})

/**
 * The `ExitPlanMode` result row.
 *
 * It lost both of the two things it used to state: the words "Plan approved", and the
 * file the command line interface wrote the plan to. Its header read `default`, which
 * is the bare wire token for the mode the session RETURNS to.
 */
describe('claude ExitPlanMode rows', () => {
  const useRow = (name: string, toolInput: Record<string, unknown> = {}) => ({
    type: 'assistant',
    message: { role: 'assistant', content: [{ type: 'tool_use', id: 'toolu_1', name, input: toolInput }] },
  })

  const resultRow = (payload: Record<string, unknown>, isError = false) => ({
    type: 'user',
    message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: isError ? 'Try a smaller scope.' : 'ok', is_error: isError }] },
    tool_use_result: payload,
  })

  function approvedCall(payload: Record<string, unknown>) {
    const row = providerRow(AgentProvider.CLAUDE_CODE, useRow(CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE), {
      role: 'request',
      result: input(resultRow(payload)),
    })
    return row?.kind === 'tool' ? row.call : null
  }

  it('heads an approved plan with words rather than the mode token', () => {
    const call = approvedCall({ filePath: '/repo/.plans/one.md' })
    expect(call?.title).toBe('Plan approved')
    expect(call?.kind === 'switch_mode' && call.request.mode).toBeUndefined()
  })

  it('states the plan file the interface wrote', () => {
    expect(approvedCall({ filePath: '/repo/.plans/one.md' })?.metadata)
      .toEqual([{ label: 'Plan file', value: '/repo/.plans/one.md' }])
  })

  it('states no plan file when the result carries none', () => {
    expect(approvedCall({})?.metadata).toBeUndefined()
  })

  // The refusal is an ANSWER the agent asked for, and the kind words that outcome
  // "Sent feedback" from the status rather than from a title.
  it('words a refused plan as declined and keeps the feedback', () => {
    const row = providerRow(AgentProvider.CLAUDE_CODE, useRow(CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE), {
      role: 'request',
      result: input(resultRow({}, true)),
    })
    const call = row?.kind === 'tool' ? row.call : null
    expect(call?.status).toBe('declined')
    expect(call?.result).toEqual({ text: 'Try a smaller scope.', format: 'plain' })
    expect(call?.metadata).toBeUndefined()
  })

  // A worktree move still states its mode, which is what tells entering one from
  // leaving one: both used to state `worktree`, so the two rows drew one title.
  it.each([
    [CLAUDE_TOOL_NAMES.ENTER_WORKTREE, 'worktree'],
    [CLAUDE_TOOL_NAMES.EXIT_WORKTREE, 'default'],
  ])('keeps the mode and the target of a %s call', (name, mode) => {
    const row = providerRow(AgentProvider.CLAUDE_CODE, useRow(name, { name: 'feature-a' }), { role: 'request' })
    const call = row?.kind === 'tool' ? row.call : null
    expect(call?.kind === 'switch_mode' && call.request).toEqual({ mode, target: 'feature-a' })
  })
})
