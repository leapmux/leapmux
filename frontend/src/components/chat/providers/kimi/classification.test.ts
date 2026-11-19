import { describe, expect, it } from 'vitest'
import { KIMI_EVENT, KIMI_NOTIFICATION_EVENT, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiConversationItemRecord, kimiConversationToolRecord, kimiConversationTurnRecord, kimiFrame, kimiTranscriptMarkerFrame, kimiTranscriptToolFrame, kimiTranscriptTurnFrame } from '~/test-support/kimiFixtures'
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
  it('draws the public proposal exactly once across the tool span', () => {
    const display = { kind: 'plan_review', plan: '# Native plan\n\n1. Keep the exact proposal.\n' }
    const request = kimiConversationToolRecord('request', { name: KIMI_TOOL.ExitPlanMode, display })
    const result = kimiConversationToolRecord('result', {
      name: KIMI_TOOL.ExitPlanMode,
      status: 'ended',
      outcome: 'completed',
      display: {
        ...display,
        review: { state: 'approved' },
      },
      output: 'Approved',
    })
    expect(classify(request)).toStrictEqual({ kind: 'assistant_plan' })
    expect(classify(result)).toStrictEqual({ kind: 'hidden' })
    expect(classify(kimiConversationToolRecord('result', { name: KIMI_TOOL.ExitPlanMode, display }), { completion: MessageCompletion.INTERRUPTED })).toStrictEqual({ kind: 'hidden' })
    expect(classify(kimiConversationToolRecord('request', { name: KIMI_TOOL.ExitPlanMode, input: {} }))).toStrictEqual({ kind: 'tool_use' })
    expect(classify(kimiConversationToolRecord('request', { name: KIMI_TOOL.Bash, display }))).toStrictEqual({ kind: 'tool_use' })
  })

  it('renders a goal opener as a notice when the worker retains it for that role', () => {
    const record = kimiConversationItemRecord({
      type: 'user_message',
      status: 'ended',
      outcome: 'completed',
      prompt_ids: [],
      placement: 'opener',
      origin: { kind: 'goal' },
      text: 'Native prompt text',
      attachments: [],
      skills: [],
    })
    expect(classify({ ...record, role: 'origin' })).toMatchObject({
      kind: 'notification',
      entries: [
        { kind: 'text', text: 'Continuing the goal' },
      ],
    })
    expect(classify(record)).toStrictEqual({ kind: 'user_text' })
  })

  it('classifies a public task outcome as a notification', () => {
    const task = kimiConversationItemRecord({
      type: 'notification',
      status: 'ended',
      outcome: 'completed',
      notification_id: 'task-1-finished',
      notification_type: 'task.completed',
      source: { kind: 'background_task', id: 'task-1' },
      title: 'Task finished',
      body: 'The work passed.',
      severity: 'info',
    })
    expect(classify(task)).toStrictEqual({
      kind: 'notification',
      entries: [
        { kind: 'text', text: 'Task finished: The work passed.' },
      ],
    })
    expect(classifyThread([task])).toStrictEqual(classify(task))
  })

  it('reads native requests, ended results and retained running results by role', () => {
    const item = {
      name: KIMI_TOOL.Bash,
      input: { command: 'ls' },
    }
    const request = kimiConversationToolRecord('request', item)
    expect(classify(request)).toStrictEqual({ kind: 'tool_use' })
    expect(classify(request, { completion: MessageCompletion.INTERRUPTED })).toStrictEqual({ kind: 'tool_use' })
    for (const outcome of ['completed', 'failed'])
      expect(classify(kimiConversationToolRecord('result', { ...item, status: 'ended', outcome }))).toStrictEqual({ kind: 'tool_result' })
    expect(classify(kimiConversationToolRecord('result', item), { completion: MessageCompletion.INTERRUPTED })).toStrictEqual({ kind: 'tool_result' })
  })

  it('keeps an obsolete tool envelope outside the native tool path', () => {
    expect(classify(kimiTranscriptToolFrame({
      name: KIMI_TOOL.Bash,
      input: { command: 'ls' },
    }))).toStrictEqual({ kind: 'unknown' })
  })

  it('keeps an obsolete completed turn outside the divider path', () => {
    expect(classify(kimiTranscriptTurnFrame({ state: 'completed' }))).toStrictEqual({ kind: 'unknown' })
  })

  it('hides every local closing copy and preserves a mode switch without a plan', () => {
    const display = { kind: 'plan_review', plan: '# Plan\n1. Do it.' }
    const closingCopy = kimiConversationToolRecord('result', { name: KIMI_TOOL.ExitPlanMode, input: {}, display })
    for (const completion of [MessageCompletion.COMPLETE, MessageCompletion.INTERRUPTED, MessageCompletion.ERROR])
      expect(classify(closingCopy, { completion, spanType: KIMI_TOOL.ExitPlanMode }), String(completion)).toStrictEqual({ kind: 'hidden' })
    const modeSwitch = kimiConversationToolRecord('result', { name: KIMI_TOOL.ExitPlanMode, input: {} })
    expect(classify(modeSwitch, { completion: MessageCompletion.INTERRUPTED })).toStrictEqual({ kind: 'tool_result' })
  })

  it('reads a native turn end as the divider', () => {
    expect(classify(kimiConversationTurnRecord({ status: 'ended', outcome: 'completed' }))).toStrictEqual({ kind: 'result_divider' })
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
    expect(classify(kimiConversationTurnRecord({}))).toStrictEqual({ kind: 'hidden' })
  })

  it('classifies events without a current stored reader as unknown', () => {
    for (const type of [
      KIMI_EVENT.Agent,
      KIMI_EVENT.TaskList,
      'future.event',
    ])
      expect(classify(kimiFrame(type)), type).toStrictEqual({ kind: 'unknown' })
  })

  it('hides a running compaction and rejects an obsolete start marker', () => {
    const start = kimiConversationItemRecord({ type: 'system', kind: 'compaction', trigger: 'manual' })
    expect(classify(start)).toStrictEqual({ kind: 'hidden' })
    expect(classify(kimiTranscriptMarkerFrame('compaction', { phase: 'started' }))).toStrictEqual({ kind: 'unknown' })
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
        kimiConversationItemRecord({
          type: 'system',
          kind: 'compaction',
          status: 'ended',
          outcome: 'completed',
          tokens_before: 9000,
          tokens_after: 1200,
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
    const userTurn = kimiConversationTurnRecord({})
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
