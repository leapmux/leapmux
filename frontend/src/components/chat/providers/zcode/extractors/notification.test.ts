import { describe, expect, it } from 'vitest'
import { ZCODE_METHOD, ZCODE_STATE_REASON } from '~/generated/contracts/zcode-protocol'
import { zcodeNotificationEntry } from './notification'

function state(reason: string): Record<string, unknown> {
  return { method: ZCODE_METHOD.StateUpdated, params: { scope: 'session', sessionId: 's-1', reason, patch: {} } }
}

describe('zcodeNotificationEntry', () => {
  it('reads the native compaction start and completion', () => {
    expect(zcodeNotificationEntry(state(ZCODE_STATE_REASON.CompactStarted))).toEqual([{ kind: 'compaction', phase: 'start' }])
    expect(zcodeNotificationEntry(state(ZCODE_STATE_REASON.SessionCompacted))).toEqual([{ kind: 'compaction', phase: 'end', detail: {} }])
  })

  it('states a native failure or cancellation without a success boundary', () => {
    expect(zcodeNotificationEntry(state(ZCODE_STATE_REASON.SessionCompactFailed))).toEqual([{ kind: 'compaction', phase: 'end', error: 'the provider reported a failure' }])
    expect(zcodeNotificationEntry(state(ZCODE_STATE_REASON.SessionCompactCancelled))).toEqual([{ kind: 'compaction', phase: 'end', error: 'cancelled' }])
  })

  it('ignores a settings update that is not about compaction', () => {
    expect(zcodeNotificationEntry(state('model_changed'))).toEqual([])
  })
})
