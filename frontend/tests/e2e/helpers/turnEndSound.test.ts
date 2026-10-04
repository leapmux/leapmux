import { describe, expect, it } from 'vitest'
import { AgentActivityState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { parseSettledReceipt, selectIdleReceipt } from './turnEndSound'

describe('parseSettledReceipt', () => {
  it('preserves explicit zero and the absence of a count', () => {
    const zero = parseSettledReceipt({ agentId: 'a1', state: AgentActivityState.IDLE, numToolUses: 0 })
    const absent = parseSettledReceipt({ agentId: 'a1', state: AgentActivityState.IDLE })
    expect(zero.numToolUses).toBe(0)
    expect(Object.hasOwn(zero, 'numToolUses')).toBe(true)
    expect(Object.hasOwn(absent, 'numToolUses')).toBe(false)
  })

  it('rejects malformed counts and missing agent or settled state', () => {
    for (const value of [null, {}, { agentId: '', state: AgentActivityState.IDLE }, { agentId: 'a1', state: AgentActivityState.WORKING }])
      expect(() => parseSettledReceipt(value)).toThrow()
    for (const count of [-1, 0.5, null, undefined, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1])
      expect(() => parseSettledReceipt({ agentId: 'a1', state: AgentActivityState.IDLE, numToolUses: count })).toThrow('tool count')
  })
})

describe('selectIdleReceipt', () => {
  it('excludes an old idle edge, another agent, and a waiting transition', () => {
    const receipts = [
      { agentId: 'a1', state: AgentActivityState.IDLE, numToolUses: 1 },
      { agentId: 'a2', state: AgentActivityState.IDLE, numToolUses: 0 },
      { agentId: 'a1', state: AgentActivityState.WAITING_FOR_USER },
      { agentId: 'a1', state: AgentActivityState.IDLE, numToolUses: 0 },
    ]
    expect(selectIdleReceipt(receipts, { agentId: 'a1', after: 1 })).toEqual(receipts[3])
    expect(selectIdleReceipt(receipts, { agentId: 'a1', after: 4 })).toBeUndefined()
  })

  it('rejects an invalid cursor or agent ID', () => {
    for (const after of [-1, 0.5, Number.MAX_SAFE_INTEGER + 1])
      expect(() => selectIdleReceipt([], { agentId: 'a1', after })).toThrow('boundary')
    expect(() => selectIdleReceipt([], { agentId: '', after: 0 })).toThrow('boundary')
  })
})
