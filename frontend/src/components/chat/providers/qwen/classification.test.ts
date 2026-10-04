import { describe, expect, it } from 'vitest'
import { qwenAgentTurnEnd } from './classification'

describe('qwenAgentTurnEnd', () => {
  it('reads the reason of _qwencode/end_turn', () => {
    expect(qwenAgentTurnEnd({ method: '_qwencode/end_turn', params: { reason: 'end_turn' } })).toBe('end_turn')
    expect(qwenAgentTurnEnd({ method: '_qwencode/end_turn', params: { reason: 'max_tokens' } })).toBe('max_tokens')
  })

  it('answers the empty reason for a turn end that states none', () => {
    expect(qwenAgentTurnEnd({ method: '_qwencode/end_turn', params: {} })).toBe('')
    expect(qwenAgentTurnEnd({ method: '_qwencode/end_turn' })).toBe('')
    expect(qwenAgentTurnEnd({ method: '_qwencode/end_turn', params: { reason: null } })).toBe('')
  })

  // The reason is wire data. A value that is no string states no reason, and the
  // frame still ends the turn.
  it('answers the empty reason for a reason that is no string, or params that are no object', () => {
    expect(qwenAgentTurnEnd({ method: '_qwencode/end_turn', params: { reason: 5 } })).toBe('')
    expect(qwenAgentTurnEnd({ method: '_qwencode/end_turn', params: { reason: { code: 'end_turn' } } })).toBe('')
    expect(qwenAgentTurnEnd({ method: '_qwencode/end_turn', params: 'end_turn' })).toBe('')
    expect(qwenAgentTurnEnd({ method: '_qwencode/end_turn', params: ['end_turn'] })).toBe('')
  })

  it('answers undefined for every other frame', () => {
    expect(qwenAgentTurnEnd({ method: '_qwencode/start_turn', params: { reason: 'x' } })).toBeUndefined()
    expect(qwenAgentTurnEnd({ stopReason: 'end_turn' })).toBeUndefined()
    expect(qwenAgentTurnEnd({ sessionUpdate: 'session_info_update', method: undefined })).toBeUndefined()
    expect(qwenAgentTurnEnd({}), 'an empty frame').toBeUndefined()
  })
})
