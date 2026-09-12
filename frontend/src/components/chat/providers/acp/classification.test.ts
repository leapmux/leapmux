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
