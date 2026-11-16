import { describe, expect, it } from 'vitest'
import { KIMI_EVENT, KIMI_NOTIFICATION_EVENT, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiFrame, kimiTranscriptFrame, kimiTranscriptMarkerFrame, kimiTranscriptToolFrame, kimiTranscriptTurnFrame } from '~/test-support/kimiFixtures'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { classifyKimiMessage } from './classification'
import '../index'

function classify(parent: Record<string, unknown>, extra: { completion?: MessageCompletion, spanType?: string } = {}) {
  return classifyKimiMessage({
    ...input(parent, undefined, AgentProvider.KIMI_CODE),
    ...extra,
  })
}

describe('classifyKimiMessage', () => {
  it('classifies native start explanations and task text as notifications', () => {
    for (const operation of [
      {
        op: 'turn.upsert',
        turn: {
          kind: 'turn',
          turnId: 't0',
          state: 'running',
          origin: {
            kind: 'cron',
            payload: { kind: 'cron_job', name: 'daily' },
          },
        },
      },
      {
        op: 'frame.upsert',
        turnId: 't0',
        stepId: 't0.1',
        frame: {
          kind: 'text',
          frameId: 'f1',
          role: 'user',
          taskId: 'task-1',
          text: 'Task finished',
        },
      },
    ]) {
      expect(classify(kimiTranscriptFrame(operation))).toMatchObject({
        kind: 'notification',
        entries: [
          { kind: 'text' },
        ],
      })
    }
  })

  it('classifies native tool states and retained running frames', () => {
    const frame = {
      name: 'Bash',
      input: { command: 'printf ok' },
    }
    expect(classify(kimiTranscriptToolFrame(frame))).toStrictEqual({ kind: 'tool_use' })
    for (const state of ['done', 'error'])
      expect(classify(kimiTranscriptToolFrame({ ...frame, state, output: 'result' }))).toStrictEqual({ kind: 'tool_result' })
    expect(classify(kimiTranscriptToolFrame(frame), { completion: MessageCompletion.INTERRUPTED })).toStrictEqual({ kind: 'tool_result' })
  })

  it('renders a native plan once and hides its closing copy', () => {
    const frame = {
      name: 'ExitPlanMode',
      input: {},
      display: { kind: 'plan_review', plan: '# Native plan' },
    }
    expect(classify(kimiTranscriptToolFrame(frame))).toStrictEqual({ kind: 'assistant_plan' })
    expect(classify(kimiTranscriptToolFrame({ ...frame, state: 'done', output: 'approved' }))).toStrictEqual({ kind: 'hidden' })
    expect(classify(kimiTranscriptToolFrame(frame), { completion: MessageCompletion.INTERRUPTED })).toStrictEqual({ kind: 'hidden' })
  })

  it('reads a completed native transcript turn as the divider', () => {
    expect(classify({
      type: 'transcript.ops',
      session_id: 'session_1',
      payload: {
        type: 'transcript.ops',
        agent_id: 'main',
        seq: 8,
        ops: [
          {
            op: 'turn.upsert',
            turn: {
              kind: 'turn',
              turnId: 't0',
              ordinal: 0,
              state: 'completed',
              origin: { kind: 'user' },
            },
          },
        ],
      },
    })).toStrictEqual({ kind: 'result_divider' })
  })

  it('reads a call start as a request and its result as the result', () => {
    expect(classify(kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.Bash,
      input: { command: 'ls' },
    }))).toStrictEqual({ kind: 'tool_use' })
    expect(classify(kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.Bash,
      input: { command: 'ls' },
      state: 'done',
      output: 'a.go',
    }), { spanType: KIMI_TOOL.Bash })).toStrictEqual({ kind: 'tool_result' })
  })

  it('reads a retained start as the result of its call', () => {
    expect(classify(kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.Bash,
      input: { command: 'sleep 9' },
    }), { completion: MessageCompletion.INTERRUPTED })).toStrictEqual({ kind: 'tool_result' })
  })

  it('draws the plan an ExitPlanMode call proposes and hides its result', () => {
    const start = kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.ExitPlanMode,
      input: {},
      display: { kind: 'plan_review', plan: '# Plan\n1. Do it.' },
    })
    expect(classify(start)).toStrictEqual({ kind: 'assistant_plan' })
    expect(classify(kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.ExitPlanMode,
      input: {},
      state: 'done',
      output: 'Exited plan mode.',
      display: { kind: 'plan_review', plan: '# Plan\n1. Do it.' },
    }), { spanType: KIMI_TOOL.ExitPlanMode })).toStrictEqual({ kind: 'hidden' })
    // With no plan the call is an ordinary mode switch.
    expect(classify(kimiTranscriptToolFrame({ toolCallId: 'c', name: KIMI_TOOL.ExitPlanMode, input: {} }))).toStrictEqual({ kind: 'tool_use' })
  })

  // A turn that ended while the plan waited for its answer stores the start AGAIN as
  // the call's closing row. The start already drew the plan, and the closing row of a
  // plan call is hidden, so the copy must not draw the plan a second time.
  it('hides the retained copy of a plan start', () => {
    const start = kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.ExitPlanMode,
      input: {},
      display: { kind: 'plan_review', plan: '# Plan\n1. Do it.' },
    })
    for (const completion of [MessageCompletion.COMPLETE, MessageCompletion.INTERRUPTED, MessageCompletion.ERROR])
      expect(classify(start, { completion, spanType: KIMI_TOOL.ExitPlanMode }), String(completion)).toStrictEqual({ kind: 'hidden' })
    // A retained mode switch that proposed no plan still closes its call as a result.
    expect(classify(kimiTranscriptToolFrame({ toolCallId: 'c', name: KIMI_TOOL.ExitPlanMode, input: {} }), { completion: MessageCompletion.INTERRUPTED })).toStrictEqual({ kind: 'tool_result' })
  })

  it('reads a turn end as the divider', () => {
    expect(classify(kimiTranscriptTurnFrame({ state: 'completed' }))).toStrictEqual({ kind: 'result_divider' })
  })

  it('reads a notice as a notification', () => {
    const category = classify(kimiFrame(KIMI_EVENT.Warning, { message: 'Low disk space' }))
    expect(category).toStrictEqual({
      kind: 'notification',
      entries: [
        { kind: 'text', text: 'Warning: Low disk space' },
      ],
    })
  })

  it('hides a notice it cannot word, and a user turn', () => {
    expect(classify(kimiFrame(KIMI_EVENT.Warning, {}))).toStrictEqual({ kind: 'hidden' })
    expect(classify(kimiTranscriptTurnFrame({
      origin: { kind: 'user' },
    }))).toStrictEqual({ kind: 'hidden' })
  })

  it('classifies events without a current stored reader as unknown', () => {
    for (const type of [
      KIMI_EVENT.Agent,
      KIMI_EVENT.TaskList,
      'future.event',
    ])
      expect(classify(kimiFrame(type)), type).toStrictEqual({ kind: 'unknown' })
  })

  it('hides a native compaction start and identifies it for the shared history filter', () => {
    const start = kimiTranscriptMarkerFrame('compaction', { phase: 'started', trigger: 'manual' })
    expect(classify(start)).toStrictEqual({ kind: 'hidden' })
    expect(providerFor(AgentProvider.KIMI_CODE)?.transcript.compactionStart?.(start)).toBe(true)
    expect(providerFor(AgentProvider.KIMI_CODE)?.transcript.compactionStart?.(kimiTranscriptMarkerFrame('compaction', { phase: 'completed' }))).toBe(false)
  })

  it('reads the rows the service layer writes', () => {
    expect(classify({ content: 'Hello.' })).toStrictEqual({ kind: 'user_content' })
    expect(classify({ content: 'Hidden.', hidden: true })).toStrictEqual({ kind: 'user_content' })
    expect(classify({ content: 'Run the plan.', planExecution: true })).toStrictEqual({ kind: 'plan_execution' })
    expect(classify({ type: 'interrupted' }).kind).toBe('notification')
    expect(classify({ type: 'something else' })).toStrictEqual({ kind: 'unknown' })
  })

  it('threads consecutive notices', () => {
    const wrapper = {
      old_seqs: [1, 2],
      messages: [
        kimiFrame(KIMI_EVENT.Warning, { message: 'A native warning.' }),
        kimiTranscriptMarkerFrame('compaction', {
          phase: 'completed',
          result: { tokensBefore: 9000, tokensAfter: 1200 },
        }),
      ],
    }
    expect(classifyThread(wrapper.messages)).toStrictEqual({
      kind: 'notification',
      entries: [
        { kind: 'text', text: 'Warning: A native warning.' },
        {
          kind: 'compaction',
          phase: 'end',
          detail: { pre: 9000, post: 1200 },
        },
      ],
    })
    expect(classifyThread([])).toStrictEqual({ kind: 'hidden' })
  })

  it('keeps the worded entries of a thread and hides a thread that words nothing', () => {
    const silent = kimiFrame(KIMI_EVENT.Warning, {})
    const userTurn = kimiTranscriptTurnFrame({
      origin: { kind: 'user' },
    })
    expect(classifyThread([
      silent,
      kimiFrame(KIMI_EVENT.Warning, { message: 'Low disk' }),
    ]))
      .toStrictEqual({
        kind: 'notification',
        entries: [
          { kind: 'text', text: 'Warning: Low disk' },
        ],
      })
    expect(classifyThread([silent, userTurn])).toStrictEqual({ kind: 'hidden' })
  })

  it('reads a thread of LeapMux notices as the entries each notice states', () => {
    const notice = { type: 'interrupted' }
    const single = classify(notice)
    expect(single.kind).toBe('notification')
    expect(classifyThread([notice])).toStrictEqual(single)
  })

  it('reads a wrapper that is no notice thread by its parent, and no row at all as unknown', () => {
    expect(classifyThread([
      { content: 'Hello.' },
    ])).toStrictEqual({ kind: 'unknown' })
    expect(classifyKimiMessage(input(undefined, undefined, AgentProvider.KIMI_CODE))).toStrictEqual({ kind: 'unknown' })
  })

  it('words a turn the agent started by itself', () => {
    expect(classify(kimiTranscriptTurnFrame({
      origin: {
        kind: 'other',
        payload: { kind: 'system_trigger', name: 'goal_continuation' },
      },
    })))
      .toStrictEqual({
        kind: 'notification',
        entries: [
          { kind: 'text', text: 'Continuing the goal' },
        ],
      })
  })

  it('reads every notice type it records as a notification when the notice words something', () => {
    const worded: Record<string, Record<string, unknown>> = {
      [KIMI_EVENT.ConfigWarning]: {
        warnings: [
          { message: 'Warning.' },
        ],
      },
      [KIMI_EVENT.Warning]: { message: 'w' },
    }
    expect(Object.keys(worded).sort()).toStrictEqual([...KIMI_NOTIFICATION_EVENT].sort())
    for (const [type, fields] of Object.entries(worded)) {
      const category = classify(kimiFrame(type, fields))
      expect(category.kind, type).toBe('notification')
      expect(category.kind === 'notification' && category.entries.length, type).toBe(1)
    }
  })
})

/** One consolidated thread of rows, as the store hands it to the classifier. */
function classifyThread(messages: unknown[]) {
  return classifyKimiMessage(input(undefined, {
    old_seqs: messages.map((_, index) => index + 1),
    messages,
  }, AgentProvider.KIMI_CODE))
}
