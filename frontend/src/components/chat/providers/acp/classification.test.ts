import { describe, expect, it } from 'vitest'
import { input } from '../testUtils'
import { classifyACPMessage } from './classification'

describe('mode updates (ACP)', () => {
  it('keeps native mode metadata out of the visible transcript', () => {
    const classify = classifyACPMessage()
    expect(classify(input({ sessionUpdate: 'current_mode_update', currentModeId: 'plan' }))).toEqual({ kind: 'hidden' })
  })
})

// A row LeapMux itself wrote carries a `type` and no session update, so the ACP
// dispatch finds nothing of its own in it. Every provider renders these the same way,
// which is why one predicate answers for all of them.
describe('rows LeapMux itself wrote (ACP)', () => {
  it('classifies each plain notification type as a notification', () => {
    const classify = classifyACPMessage()
    for (const type of ['interrupted', 'settings_changed', 'context_cleared', 'agent_error', 'plan_updated', 'compacting'])
      expect(classify(input({ type }))).toEqual({ kind: 'notification', messages: [{ type }] })
  })

  it('leaves an unknown type to the fallback', () => {
    const classify = classifyACPMessage()
    expect(classify(input({ type: 'not_a_notification' }))).toEqual({ kind: 'unknown' })
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
