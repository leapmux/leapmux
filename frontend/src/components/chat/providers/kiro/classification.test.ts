import { describe, expect, it } from 'vitest'
import { kiroAgentTurnEnd } from './classification'

/** Kiro's end of a turn, as the worker stores it. */
function turnEnd(kiro: Record<string, unknown>) {
  return { sessionUpdate: 'session_info_update', _meta: { kiro: { kind: 'turn_end', ...kiro } } }
}

describe('kiroAgentTurnEnd', () => {
  it('reads the stop reason of a turn_end', () => {
    expect(kiroAgentTurnEnd(turnEnd({ stopReason: 'end_turn', turnEnd: { stopReason: 'end_turn' }, messageId: 'm' }))).toBe('end_turn')
    expect(kiroAgentTurnEnd(turnEnd({ stopReason: 'max_tokens' }))).toBe('max_tokens')
  })

  it('answers the empty reason for a turn_end that states none', () => {
    expect(kiroAgentTurnEnd(turnEnd({}))).toBe('')
    expect(kiroAgentTurnEnd(turnEnd({ stopReason: 7 }))).toBe('')
  })

  it('answers undefined for every other frame', () => {
    expect(kiroAgentTurnEnd({ sessionUpdate: 'session_info_update', _meta: { kiro: { kind: 'turn_start' } } })).toBeUndefined()
    expect(kiroAgentTurnEnd({ sessionUpdate: 'session_info_update' })).toBeUndefined()
    expect(kiroAgentTurnEnd({ sessionUpdate: 'agent_message_chunk', _meta: { kiro: { kind: 'turn_end' } } })).toBeUndefined()
    expect(kiroAgentTurnEnd({ stopReason: 'end_turn' })).toBeUndefined()
  })
})
