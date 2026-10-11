import { describe, expect, it } from 'vitest'
import { grokAgentTurnEnd } from './classification'

function notification(update: Record<string, unknown>, method = '_x.ai/session_notification') {
  return { jsonrpc: '2.0', method, params: { sessionId: 's', update } }
}

describe('grokAgentTurnEnd', () => {
  it('reads the stop reason of turn_completed', () => {
    expect(grokAgentTurnEnd(notification({ sessionUpdate: 'turn_completed', stop_reason: 'end_turn' }))).toBe('end_turn')
    expect(grokAgentTurnEnd(notification({ sessionUpdate: 'turn_completed', stop_reason: 'error' }))).toBe('error')
  })

  it('answers the empty reason for a turn end that states none', () => {
    expect(grokAgentTurnEnd(notification({ sessionUpdate: 'turn_completed' }))).toBe('')
    expect(grokAgentTurnEnd(notification({ sessionUpdate: 'turn_completed', stop_reason: 7 }))).toBe('')
  })

  it('answers undefined for every other frame', () => {
    expect(grokAgentTurnEnd(notification({ sessionUpdate: 'turn_started' }))).toBeUndefined()
    expect(grokAgentTurnEnd(notification({ sessionUpdate: 'turn_completed' }, '_x.ai/other'))).toBeUndefined()
    expect(grokAgentTurnEnd({ stopReason: 'end_turn' })).toBeUndefined()
    expect(grokAgentTurnEnd({ method: '_x.ai/session_notification' })).toBeUndefined()
  })
})
