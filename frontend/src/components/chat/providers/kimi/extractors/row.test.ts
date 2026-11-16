import type { MessageCategory } from '../../../messageClassifier'
import type { ChatRow, ToolCallRow } from '../../../model/row'
import type { ResolvedMessageContent, ToolSpanContext } from '../../../rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { KIMI_DISPLAY, KIMI_EVENT, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiFrame, kimiTranscriptToolFrame, kimiTranscriptTurnFrame } from '~/test-support/kimiFixtures'
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
  /** The span rows that the loaded window shows. It defaults to the request the test states. */
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

const PLAN_START = kimiTranscriptToolFrame({
  toolCallId: CALL,
  name: KIMI_TOOL.ExitPlanMode,
  input: {},
  display: { kind: KIMI_DISPLAY.PlanReview, plan: '# Plan\n1. Do it.' },
})

describe('kimiExtractRow', () => {
  it('reads the plan an ExitPlanMode call proposes as the plan row', () => {
    expect(extract(PLAN_START, { kind: 'assistant_plan' })).toStrictEqual({ kind: 'assistant-plan', text: '# Plan\n1. Do it.' })
  })

  it('answers null for a plan category whose row states no plan', () => {
    const blank = kimiTranscriptToolFrame({
      toolCallId: CALL,
      name: KIMI_TOOL.ExitPlanMode,
      input: {},
      display: { kind: KIMI_DISPLAY.PlanReview, plan: '   ' },
    })
    expect(extract(blank, { kind: 'assistant_plan' })).toBeNull()
    expect(extract(kimiTranscriptToolFrame({
      toolCallId: CALL,
      name: KIMI_TOOL.ExitPlanMode,
      input: {},
      state: 'done',
      output: '# Plan',
    }), { kind: 'assistant_plan' })).toBeNull()
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
    ] as const) {
      expect(extract(kimiTranscriptToolFrame({
        toolCallId: CALL,
        name: KIMI_TOOL.Bash,
        input: { command: 'ls' },
      }), { kind } as MessageCategory), kind).toBeNull()
    }
  })

  it('answers null for a tool category whose row is no tool event', () => {
    expect(extract({ content: 'Hello.' }, { kind: 'tool_use' })).toBeNull()
    expect(extract(kimiTranscriptTurnFrame({ state: 'completed' }), { kind: 'tool_result' })).toBeNull()
  })

  it('reads each tool row from its own frame and only earlier request context', () => {
    const start = kimiTranscriptToolFrame({
      toolCallId: CALL,
      name: KIMI_TOOL.Bash,
      input: { command: 'ls' },
    })
    const result = kimiTranscriptToolFrame({
      toolCallId: CALL,
      name: KIMI_TOOL.Bash,
      input: { command: 'ls' },
      state: 'done',
      output: 'a.go\n',
    })
    const request = toolRow(extract(start, { kind: 'tool_use' }, { request: start }))
    expect(request.role).toBe('request')
    expect(request.call.status).toBe('in_progress')
    expect(request.call.result).toBeUndefined()
    const answer = toolRow(extract(result, { kind: 'tool_result' }, { request: start }))
    expect(answer).toMatchObject({ role: 'result', hasRequestRow: true })
    expect(answer.call.request).toStrictEqual({ command: 'ls' })
    expect(answer.call.result).toStrictEqual({
      commands: [
        { output: 'a.go\n', exitCode: 0 },
      ],
      unresolvedTerminals: [],
    })
  })

  it('states the run of a subagent launch on its result row alone', () => {
    const args = { description: 'Survey', prompt: 'Read the code.' }
    const start = kimiTranscriptToolFrame({ toolCallId: CALL, name: KIMI_TOOL.Agent, input: args })
    const result = kimiTranscriptToolFrame({
      toolCallId: CALL,
      name: KIMI_TOOL.Agent,
      input: args,
      state: 'done',
      output: 'agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nDone.',
    })
    const request = toolRow(extract(start, { kind: 'tool_use' }, { request: start }))
    expect(request.call.kind).toBe('agent')
    expect(request.call.status).toBe('in_progress')
    expect(request.call.request).toMatchObject(args)
    expect(request.call.result).toBeUndefined()
    const answer = toolRow(extract(result, { kind: 'tool_result' }, { request: start }))
    expect(answer.call.result).toMatchObject({
      agents: [
        { agentId: 'agent-0', outcome: 'completed', body: 'Done.' },
      ],
    })
  })

  it('counts no unrelated request as the earlier side of a result', () => {
    const result = kimiTranscriptToolFrame({
      toolCallId: CALL,
      name: KIMI_TOOL.Bash,
      input: { command: 'ls' },
      state: 'done',
      output: 'a.go',
    })
    for (const request of [
      kimiFrame(KIMI_EVENT.Warning, { message: 'Low disk' }),
      kimiTranscriptToolFrame({
        toolCallId: 'other',
        name: KIMI_TOOL.Bash,
        input: { command: 'ls' },
      }),
      kimiTranscriptToolFrame({
        toolCallId: CALL,
        name: KIMI_TOOL.Bash,
        input: { command: 'ls' },
      }, 'child'),
    ]) {
      expect(toolRow(extract(result, { kind: 'tool_result' }, { request }))).toMatchObject({ role: 'result', hasRequestRow: false })
    }
  })

  it('counts no earlier request that the loaded window does not show', () => {
    const start = kimiTranscriptToolFrame({
      toolCallId: CALL,
      name: KIMI_TOOL.Bash,
      input: { command: 'ls' },
    })
    const result = kimiTranscriptToolFrame({
      toolCallId: CALL,
      name: KIMI_TOOL.Bash,
      input: { command: 'ls' },
      state: 'done',
      output: 'a.go',
    })
    expect(toolRow(extract(result, { kind: 'tool_result' }, {
      request: start,
      visible: { request: false },
    })))
      .toMatchObject({ role: 'result', hasRequestRow: false })
  })

  it('keeps an opening frame independent from its retained closing copy', () => {
    const start = kimiTranscriptToolFrame({
      toolCallId: CALL,
      name: KIMI_TOOL.Bash,
      input: { command: 'sleep 9' },
    })
    const closing = toolRow(extract(start, { kind: 'tool_result' }, { request: start }, { completion: MessageCompletion.INTERRUPTED }))
    expect(closing).toMatchObject({ role: 'result', hasRequestRow: true })
    expect(closing.call.status).toBe('cancelled')
    const request = toolRow(extract(start, { kind: 'tool_use' }, { request: start }))
    expect(request.role).toBe('request')
    expect(request.call.status).toBe('in_progress')
    expect(request.call.result).toBeUndefined()
  })

  it('reads the completion of the row itself when the input states none', () => {
    const start = kimiTranscriptToolFrame({
      toolCallId: CALL,
      name: KIMI_TOOL.Bash,
      input: { command: 'sleep 9' },
    })
    const row = toolRow(extract(start, { kind: 'tool_result' }, { request: start }, { rowCompletion: MessageCompletion.ERROR }))
    expect(row.role).toBe('result')
    expect(row.call.status).toBe('failed')
  })
})
