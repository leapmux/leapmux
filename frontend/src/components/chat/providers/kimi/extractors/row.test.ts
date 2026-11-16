import type { MessageCategory } from '../../../messageClassifier'
import type { ChatRow, ToolCallRow } from '../../../model/row'
import type { ResolvedMessageContent, ToolSpanContext } from '../../../rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { KIMI_DISPLAY, KIMI_EVENT, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiFrame, kimiToolResult, kimiToolStart } from '~/test-support/kimiFixtures'
import { resolveMessageForRendering } from '../../registry'
import { input } from '../../testUtils'
import { kimiExtractRow } from './row'
import '../../index'

const CALL = 'call_1'
const provider = AgentProvider.KIMI_CODE

/** One row as the store resolves it, with the completion column when the test states one. */
function resolved(payload: Record<string, unknown>, completion?: MessageCompletion): ResolvedMessageContent {
  return resolveMessageForRendering({
    ...input(payload, undefined, provider),
    ...(completion !== undefined
      ? { completion }
      : {}),
  }, provider)
}

interface Sides {
  request?: Record<string, unknown>
  /** The span rows that the loaded window shows. It defaults to the side the test states. */
  visible?: Partial<ToolSpanContext['visibleRows']>
}

function extract(payload: Record<string, unknown>, category: MessageCategory, sides: Sides = {}, options: { completion?: MessageCompletion, rowCompletion?: MessageCompletion, spanType?: string } = {}): ChatRow | null {
  const span: ToolSpanContext = {
    request: sides.request && resolved(sides.request),
    role: 'other',
    visibleRows: {
      request: sides.visible?.request ?? sides.request !== undefined,
    },
  }
  return kimiExtractRow({
    resolved: resolved(payload, options.rowCompletion),
    category,
    span,
    ...(options.spanType !== undefined
      ? { spanType: options.spanType }
      : {}),
    ...(options.completion !== undefined
      ? { completion: options.completion }
      : {}),
  })
}

function toolRow(row: ChatRow | null): ToolCallRow {
  expect(row?.kind).toBe('tool')
  return row as ToolCallRow
}

const PLAN_START = kimiToolStart(CALL, KIMI_TOOL.ExitPlanMode, {}, { kind: KIMI_DISPLAY.PlanReview, plan: '# Plan\n1. Do it.' })

describe('kimiExtractRow', () => {
  it('reads the plan an ExitPlanMode call proposes as the plan row', () => {
    expect(extract(PLAN_START, { kind: 'assistant_plan' })).toStrictEqual({ kind: 'assistant-plan', text: '# Plan\n1. Do it.' })
  })

  it('answers null for a plan category whose row states no plan', () => {
    const blank = kimiToolStart(CALL, KIMI_TOOL.ExitPlanMode, {}, { kind: KIMI_DISPLAY.PlanReview, plan: '   ' })
    expect(extract(blank, { kind: 'assistant_plan' })).toBeNull()
    expect(extract(kimiToolResult(CALL, '# Plan'), { kind: 'assistant_plan' })).toBeNull()
  })

  it('reads the rows the service layer writes in the shared shape', () => {
    expect(extract({ content: 'Hello.' }, { kind: 'user_content' })).toStrictEqual({ kind: 'user', text: 'Hello.', attachments: [] })
    expect(extract({ content: '  ' }, { kind: 'user_content' })).toStrictEqual({ kind: 'hidden' })
    expect(extract({ content: 'Run the plan.', planExecution: true }, { kind: 'plan_execution' })).toStrictEqual({ kind: 'plan-execution', text: 'Run the plan.' })
  })

  it('answers null for a category it does not read', () => {
    for (const kind of [
      'assistant_text',
      'assistant_thinking',
      'result_divider',
      'hidden',
      'unknown',
    ] as const)
      expect(extract(kimiToolStart(CALL, KIMI_TOOL.Bash, { command: 'ls' }), { kind } as MessageCategory), kind).toBeNull()
  })

  it('answers null for a tool category whose row is no tool event', () => {
    expect(extract({ content: 'Hello.' }, { kind: 'tool_use' })).toBeNull()
    expect(extract(kimiFrame(KIMI_EVENT.TurnEnded, { reason: 'completed' }), { kind: 'tool_result' })).toBeNull()
  })

  // A start row reads its own frame alone, so it states no result and no finished
  // status. The result row reads its own frame and the start before it.
  it('reads a start and its result as one call, each in its own role', () => {
    const start = kimiToolStart(CALL, KIMI_TOOL.Bash, { command: 'ls' })
    const result = kimiToolResult(CALL, 'a.go\n')

    const request = toolRow(extract(start, { kind: 'tool_use' }, { request: start }))
    expect(request.role).toBe('request')
    expect(request.call.status).toBe('unstated')
    expect(request.call.result).toBeUndefined()
    expect(request.call.request).toStrictEqual({ command: 'ls' })

    const answer = toolRow(extract(result, { kind: 'tool_result' }, { request: start }, { spanType: KIMI_TOOL.Bash }))
    expect(answer.role).toBe('result')
    expect(answer).toMatchObject({ hasRequestRow: true })
    expect(answer.call.status).toBe('completed')
    expect(answer.call.request).toStrictEqual({ command: 'ls' })
    expect(answer.call.result).toStrictEqual({
      commands: [
        { output: 'a.go\n', exitCode: 0 },
      ],
      unresolvedTerminals: [],
    })
  })

  // Only the result reports the run of a subagent launch, so the start row states the
  // launch it asked for, and the result row states the run.
  it('states the run of a subagent launch on the result row alone', () => {
    const start = kimiToolStart(CALL, KIMI_TOOL.Agent, { prompt: 'Read the code.', description: 'Survey' })
    const result = kimiToolResult(CALL, 'agent_id: agent-0\nstatus: completed\n\n[summary]\nDone.')

    const request = toolRow(extract(start, { kind: 'tool_use' }, { request: start }, { spanType: KIMI_TOOL.Agent }))
    expect(request.call.kind).toBe('agent')
    expect(request.call.status).toBe('unstated')
    expect(request.call.result).toBeUndefined()
    expect(request.call.request).toMatchObject({ description: 'Survey', prompt: 'Read the code.' })

    const answer = toolRow(extract(result, { kind: 'tool_result' }, { request: start }, { spanType: KIMI_TOOL.Agent }))
    expect(answer.call.kind).toBe('agent')
    expect(answer.call.status).toBe('completed')
    expect(answer.call.request).toMatchObject({ description: 'Survey', prompt: 'Read the code.' })
    expect(answer.call.result).toMatchObject({
      agents: [
        { agentId: 'agent-0', outcome: 'completed', body: 'Done.' },
      ],
    })
  })

  // The span's request is a row of any kind, so it counts as the row beside this one
  // only when it is the start of THIS call.
  it('counts no span request that is another row or another call as the row beside it', () => {
    const notice = kimiFrame(KIMI_EVENT.Warning, { message: 'Low disk' })
    const result = kimiToolResult(CALL, 'a.go')
    expect(toolRow(extract(result, { kind: 'tool_result' }, { request: notice }, { spanType: KIMI_TOOL.Bash }))).toMatchObject({ role: 'result', hasRequestRow: false })
    expect(toolRow(extract(result, { kind: 'tool_result' }, {
      request: kimiToolStart('call_2', KIMI_TOOL.Bash, { command: 'ls' }),
    }, { spanType: KIMI_TOOL.Bash })))
      .toMatchObject({ role: 'result', hasRequestRow: false })
  })

  it('counts no span request that the loaded window does not show', () => {
    const start = kimiToolStart(CALL, KIMI_TOOL.Bash, { command: 'ls' })
    const result = kimiToolResult(CALL, 'a.go')
    expect(toolRow(extract(result, { kind: 'tool_result' }, {
      request: start,
      visible: { request: false },
    }, { spanType: KIMI_TOOL.Bash })))
      .toMatchObject({ role: 'result', hasRequestRow: false })
  })

  it('reads a retained start as the result row of its call, with how the turn ended', () => {
    const start = kimiToolStart(CALL, KIMI_TOOL.Bash, { command: 'sleep 9' })
    const row = toolRow(extract(start, { kind: 'tool_result' }, { request: start }, { completion: MessageCompletion.INTERRUPTED }))
    expect(row).toMatchObject({ role: 'result', hasRequestRow: true })
    expect(row.call.status).toBe('cancelled')
  })

  // The input's own completion wins; the row's parsed completion stands in when the
  // input states none.
  it('reads the completion of the row itself when the input states none', () => {
    const start = kimiToolStart(CALL, KIMI_TOOL.Bash, { command: 'sleep 9' })
    const row = toolRow(extract(start, { kind: 'tool_result' }, { request: start }, { rowCompletion: MessageCompletion.ERROR }))
    expect(row.role).toBe('result')
    expect(row.call.status).toBe('failed')
  })
})
