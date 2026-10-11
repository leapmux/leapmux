import { describe, expect, it } from 'vitest'
import { input } from '../testUtils'
import { classifyACPMessage } from './classification'

describe('mode updates (ACP)', () => {
  it('keeps native mode metadata out of the visible transcript', () => {
    const classify = classifyACPMessage()
    expect(classify(input({ sessionUpdate: 'current_mode_update', currentModeId: 'plan' }))).toEqual({ kind: 'hidden' })
  })
})

// Each of these reaches a surface OUTSIDE the transcript, and the worker consumes it
// rather than persisting it. They are listed here because a row an earlier build
// wrote is still in the database, and a build that stopped persisting one cannot
// reach back and remove it -- so without this rule it draws raw JSON forever.
describe('session updates the worker consumes (ACP)', () => {
  it.each([
    ['usage_update', { used: 100, size: 200000 }],
    ['available_commands_update', { availableCommands: [] }],
    ['user_message_chunk', { content: { type: 'text', text: 'hi' } }],
    ['config_option_update', { configOptions: [] }],
    // Captured from a live `goose acp` session: the runtime's own session title and
    // its modified time, which LeapMux never shows because it gives its tabs their own names.
    ['session_info_update', { title: 'A title', updatedAt: '2026-09-16T07:06:19+00:00' }],
  ])('hides %s', (sessionUpdate, fields) => {
    const classify = classifyACPMessage()
    expect(classify(input({ sessionUpdate, ...fields }))).toEqual({ kind: 'hidden' })
  })
})

// A row LeapMux itself wrote carries a `type` and no session update, so the ACP
// dispatch finds nothing of its own in it. Every provider renders these the same way,
// which is why one predicate answers for all of them.
describe('rows LeapMux itself wrote (ACP)', () => {
  it('classifies each plain notification type as a notification', () => {
    const classify = classifyACPMessage()
    const notifications = [
      { type: 'interrupted' },
      { type: 'settings_changed', changes: { model: { old: 'a', new: 'b' } } },
      { type: 'context_cleared' },
      { type: 'agent_error', error: 'failed' },
      { type: 'plan_updated', plan_title: 'Plan' },
      { type: 'compacting' },
    ]
    for (const notification of notifications)
      expect(classify(input(notification)).kind).toBe('notification')
  })

  it('leaves an unknown type to the fallback', () => {
    const classify = classifyACPMessage()
    expect(classify(input({ type: 'not_a_notification' }))).toEqual({ kind: 'unknown' })
  })
})

// The classifier reads `sessionUpdate` TWICE, and this case is why. Every token branch
// compares the string value, but the LAST branch asks whether the row carries the field
// at all: a row with a truthy `sessionUpdate` is a session update of this family,
// malformed or not, and never LeapMux's own `{content}` envelope. One read for both
// questions answers this row `user_content`, which attributes the agent's bytes to the
// reader and drops the rest of the frame. The raw-frame card states instead that
// something is wrong.
describe('a malformed session update (ACP)', () => {
  it('leaves a non-string sessionUpdate beside a content string to the fallback', () => {
    const classify = classifyACPMessage()
    expect(classify(input({ sessionUpdate: 5, content: 'hi' }))).toStrictEqual({ kind: 'unknown' })
  })
})

// The worker persists the `session/prompt` answer byte for byte, so a server
// that wraps its turn fields in a native result envelope reaches the browser
// wrapped. A classifier that read `stopReason` off the envelope alone called
// the turn end an unknown row and drew a raw JSON bubble in place of the
// turn-end divider.
describe('the native ACP result envelope', () => {
  const wrapped = (content: Record<string, unknown>) => ({
    id: 'msg-1',
    role: 'result',
    seq: 4,
    created_at: '2026-03-26T10:46:48.015Z',
    content,
  })

  it('classifies a wrapped turn end as a result divider', () => {
    const classify = classifyACPMessage()
    expect(classify(input(wrapped({ _meta: {}, stopReason: 'end_turn', usage: { totalTokens: 100 } }))))
      .toEqual({ kind: 'result_divider' })
  })

  it('leaves a wrapped non-string stopReason to the fallback', () => {
    const classify = classifyACPMessage()
    expect(classify(input(wrapped({ stopReason: 5 })))).toEqual({ kind: 'unknown' })
  })
})

// The worker wraps EVERY notification it persists into a `notification_thread`
// envelope (`wrapNotifContent`), so a notification row of this family always reaches
// the browser wrapped, and a lone notification is a one-member thread. The plan
// restart writes `context_cleared` and then `plan_execution`, and the two land in one
// thread, so the thread test answers on `context_cleared` and `plan_execution` never
// has to answer for itself. A thread that holds it alone must still classify.
describe('the plan_execution notification (ACP)', () => {
  const planExecution = { type: 'plan_execution', plan_file_path: '/p.md' }

  it('classifies a one-member plan_execution thread as a notification', () => {
    const classify = classifyACPMessage()
    const wrapper = { old_seqs: [], messages: [planExecution] }
    expect(classify(input(planExecution, wrapper)))
      .toStrictEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'Executing plan' }] })
  })

  // The thread test is what keeps the OTHER members of a thread. A per-message test
  // reads the first member alone, so it answers for a one-member thread by accident
  // and drops everything after it. `compacting` is the sibling here because the ACP
  // thread test does not accept that type either.
  it('keeps every member of a thread whose only accepted type is plan_execution', () => {
    const classify = classifyACPMessage()
    const compacting = { type: 'compacting' }
    const wrapper = { old_seqs: [], messages: [compacting, planExecution] }
    expect(classify(input(compacting, wrapper)))
      .toStrictEqual({
        kind: 'notification',
        entries: [{ kind: 'compaction', phase: 'start' }, { kind: 'text', text: 'Executing plan' }],
      })
  })

  // The per-message answer, which the classifier reaches for an unwrapped row and for
  // the FIRST member of a wrapper whose thread test found nothing. Copilot and ZCode
  // read the same predicate, so the type needs both entries and not one.
  it('classifies an unwrapped plan_execution row as a notification', () => {
    const classify = classifyACPMessage()
    expect(classify(input(planExecution)))
      .toStrictEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'Executing plan' }] })
  })
})

// The worker stores a provider's own end of an agent-started turn as the turn-end
// row, so the classifier must draw it as the divider rather than as a raw frame.
describe('agent turn ends (ACP)', () => {
  const agentTurnEnd = (parent: Record<string, unknown>) => parent.method === 'vendor/turn_ended' ? 'end_turn' : undefined

  it('classifies the provider\'s own end frame as a divider', () => {
    const classify = classifyACPMessage({ agentTurnEnd })
    expect(classify(input({ jsonrpc: '2.0', method: 'vendor/turn_ended', params: { reason: 'end_turn' } }))).toEqual({ kind: 'result_divider' })
  })

  it('leaves another frame to the rest of the classifier', () => {
    const classify = classifyACPMessage({ agentTurnEnd })
    expect(classify(input({ jsonrpc: '2.0', method: 'vendor/other', params: {} }))).toEqual({ kind: 'unknown' })
    expect(classifyACPMessage()(input({ jsonrpc: '2.0', method: 'vendor/turn_ended', params: {} })), 'with no hook the frame is not a turn end')
      .toEqual({ kind: 'unknown' })
  })

  // The hook answers undefined for "no turn end", and the empty string for a turn
  // end that states no reason. A truthiness test drops the second one, and the frame
  // then draws as a raw-frame card.
  it('classifies a turn end that states an empty stop reason as a divider', () => {
    const classify = classifyACPMessage({ agentTurnEnd: parent => parent.method === 'vendor/turn_ended' ? '' : undefined })
    expect(classify(input({ jsonrpc: '2.0', method: 'vendor/turn_ended', params: {} }))).toEqual({ kind: 'result_divider' })
  })

  // A provider can state the end on an update that the family hides otherwise.
  it('draws a turn end that a hidden update states', () => {
    const onInfo = (parent: Record<string, unknown>) => parent.sessionUpdate === 'session_info_update' && parent.turnEnd === true ? 'end_turn' : undefined
    const classify = classifyACPMessage({ agentTurnEnd: onInfo })
    expect(classify(input({ sessionUpdate: 'session_info_update', turnEnd: true }))).toEqual({ kind: 'result_divider' })
    expect(classify(input({ sessionUpdate: 'session_info_update', title: 'x' })), 'another update of that kind stays hidden').toEqual({ kind: 'hidden' })
  })
})
