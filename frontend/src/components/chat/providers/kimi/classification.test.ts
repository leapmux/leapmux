import { describe, expect, it } from 'vitest'
import { KIMI_EVENT, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiFrame, kimiToolResult, kimiToolStart } from '~/test-support/kimiFixtures'
import { input } from '../testUtils'
import { classifyKimiMessage, KIMI_NOTIFICATION_TYPES } from './classification'
import '../index'

function classify(parent: Record<string, unknown>, extra: { completion?: MessageCompletion, spanType?: string } = {}) {
  return classifyKimiMessage({ ...input(parent, undefined, AgentProvider.KIMI_CODE), ...extra })
}

describe('classifyKimiMessage', () => {
  it('reads a call start as a request and its result as the result', () => {
    expect(classify(kimiToolStart('c', KIMI_TOOL.Bash, { command: 'ls' }))).toStrictEqual({ kind: 'tool_use' })
    expect(classify(kimiToolResult('c', 'a.go'), { spanType: KIMI_TOOL.Bash })).toStrictEqual({ kind: 'tool_result' })
  })

  it('reads a retained start as the result of its call', () => {
    expect(classify(kimiToolStart('c', KIMI_TOOL.Bash, { command: 'sleep 9' }), { completion: MessageCompletion.INTERRUPTED })).toStrictEqual({ kind: 'tool_result' })
  })

  it('draws the plan an ExitPlanMode call proposes and hides its result', () => {
    const start = kimiToolStart('c', KIMI_TOOL.ExitPlanMode, {}, { kind: 'plan_review', plan: '# Plan\n1. Do it.' })
    expect(classify(start)).toStrictEqual({ kind: 'assistant_plan' })
    expect(classify(kimiToolResult('c', 'Exited plan mode.'), { spanType: KIMI_TOOL.ExitPlanMode })).toStrictEqual({ kind: 'hidden' })
    // With no plan the call is an ordinary mode switch.
    expect(classify(kimiToolStart('c', KIMI_TOOL.ExitPlanMode, {}))).toStrictEqual({ kind: 'tool_use' })
  })

  // A turn that ended while the plan waited for its answer stores the start AGAIN as
  // the call's closing row. The start already drew the plan, and the closing row of a
  // plan call is hidden, so the copy must not draw the plan a second time.
  it('hides the retained copy of a plan start', () => {
    const start = kimiToolStart('c', KIMI_TOOL.ExitPlanMode, {}, { kind: 'plan_review', plan: '# Plan\n1. Do it.' })
    for (const completion of [MessageCompletion.COMPLETE, MessageCompletion.INTERRUPTED, MessageCompletion.ERROR])
      expect(classify(start, { completion, spanType: KIMI_TOOL.ExitPlanMode }), String(completion)).toStrictEqual({ kind: 'hidden' })
    // A retained mode switch that proposed no plan still closes its call as a result.
    expect(classify(kimiToolStart('c', KIMI_TOOL.ExitPlanMode, {}), { completion: MessageCompletion.INTERRUPTED })).toStrictEqual({ kind: 'tool_result' })
  })

  it('reads a turn end as the divider', () => {
    expect(classify(kimiFrame(KIMI_EVENT.TurnEnded, { turnId: 0, reason: 'completed' }))).toStrictEqual({ kind: 'result_divider' })
  })

  it('reads a notice as a notification', () => {
    const category = classify(kimiFrame(KIMI_EVENT.Warning, { message: 'Low disk space' }))
    expect(category).toStrictEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'Warning: Low disk space' }] })
  })

  it('hides a notice it cannot word, and a user turn', () => {
    expect(classify(kimiFrame(KIMI_EVENT.Warning, {}))).toStrictEqual({ kind: 'hidden' })
    expect(classify(kimiFrame(KIMI_EVENT.TurnStarted, { origin: { kind: 'user' } }))).toStrictEqual({ kind: 'hidden' })
  })

  it('hides every other event', () => {
    for (const type of [KIMI_EVENT.AgentStatusUpdated, KIMI_EVENT.PromptSubmitted, KIMI_EVENT.ContextSpliced, KIMI_EVENT.SessionMetaUpdated])
      expect(classify(kimiFrame(type)), type).toStrictEqual({ kind: 'hidden' })
  })

  it('reads the rows the service layer writes', () => {
    expect(classify({ content: 'Hello.' })).toStrictEqual({ kind: 'user_content' })
    expect(classify({ content: 'Hidden.', hidden: true })).toStrictEqual({ kind: 'hidden' })
    expect(classify({ content: 'Run the plan.', planExecution: true })).toStrictEqual({ kind: 'plan_execution' })
    expect(classify({ type: 'interrupted' }).kind).toBe('notification')
    expect(classify({ type: 'something else' })).toStrictEqual({ kind: 'unknown' })
  })

  it('threads consecutive notices', () => {
    const wrapper = {
      old_seqs: [1, 2],
      messages: [
        kimiFrame(KIMI_EVENT.CompactionStarted, { trigger: 'auto' }),
        kimiFrame(KIMI_EVENT.CompactionCompleted, { result: { tokensBefore: 9000, tokensAfter: 1200 } }),
      ],
    }
    expect(classifyThread(wrapper.messages)).toStrictEqual({
      kind: 'notification',
      entries: [
        { kind: 'compaction', phase: 'start', detail: { trigger: 'auto' } },
        { kind: 'compaction', phase: 'end', detail: { pre: 9000, post: 1200 } },
      ],
    })
    expect(classifyThread([])).toStrictEqual({ kind: 'hidden' })
  })

  it('keeps the worded entries of a thread and hides a thread that words nothing', () => {
    const silent = kimiFrame(KIMI_EVENT.Warning, {})
    const userTurn = kimiFrame(KIMI_EVENT.TurnStarted, { origin: { kind: 'user' } })
    expect(classifyThread([silent, kimiFrame(KIMI_EVENT.Warning, { message: 'Low disk' })]))
      .toStrictEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'Warning: Low disk' }] })
    expect(classifyThread([silent, userTurn])).toStrictEqual({ kind: 'hidden' })
  })

  it('reads a thread of LeapMux notices as the entries each notice states', () => {
    const notice = { type: 'interrupted' }
    const single = classify(notice)
    expect(single.kind).toBe('notification')
    expect(classifyThread([notice])).toStrictEqual(single)
  })

  it('reads a wrapper that is no notice thread by its parent, and no row at all as unknown', () => {
    expect(classifyThread([{ content: 'Hello.' }])).toStrictEqual({ kind: 'unknown' })
    expect(classifyKimiMessage(input(undefined, undefined, AgentProvider.KIMI_CODE))).toStrictEqual({ kind: 'unknown' })
  })

  it('words a turn the agent started by itself', () => {
    expect(classify(kimiFrame(KIMI_EVENT.TurnStarted, { origin: { kind: 'system_trigger', name: 'goal_continuation' } })))
      .toStrictEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'Continuing the goal' }] })
  })

  it('reads every notice type it records as a notification when the notice words something', () => {
    const worded: Record<string, Record<string, unknown>> = {
      [KIMI_EVENT.TurnStarted]: { origin: { kind: 'retry' } },
      [KIMI_EVENT.TurnStepRetrying]: { nextAttempt: 2 },
      [KIMI_EVENT.CompactionStarted]: {},
      [KIMI_EVENT.CompactionCompleted]: {},
      [KIMI_EVENT.CompactionBlocked]: {},
      [KIMI_EVENT.CompactionCancelled]: {},
      [KIMI_EVENT.Warning]: { message: 'w' },
      [KIMI_EVENT.Error]: { message: 'e' },
      [KIMI_EVENT.TaskNotified]: { title: 't' },
    }
    expect(Object.keys(worded).sort()).toStrictEqual([...KIMI_NOTIFICATION_TYPES].sort())
    for (const [type, fields] of Object.entries(worded)) {
      const category = classify(kimiFrame(type, fields))
      expect(category.kind, type).toBe('notification')
      expect(category.kind === 'notification' && category.entries.length, type).toBe(1)
    }
  })
})

/** One consolidated thread of rows, as the store hands it to the classifier. */
function classifyThread(messages: unknown[]) {
  return classifyKimiMessage(input(undefined, { old_seqs: messages.map((_, index) => index + 1), messages }, AgentProvider.KIMI_CODE))
}
