import { describe, expect, it } from 'vitest'
import { wireSessionInfoToUpdates } from './useWorkspaceConnection'

describe('wireSessionInfoToUpdates', () => {
  it('returns an empty object for undefined or empty payloads', () => {
    expect(wireSessionInfoToUpdates(undefined)).toEqual({})
    expect(wireSessionInfoToUpdates({})).toEqual({})
  })

  it('maps snake_case wire keys to the camelCase store shape', () => {
    const updates = wireSessionInfoToUpdates({
      total_cost_usd: 1.5,
      context_usage: { input_tokens: 100 },
      codex_turn_id: 'turn-7',
      streaming_type: 'plan',
    })
    expect(updates.totalCostUsd).toBe(1.5)
    expect(updates.contextUsage).toMatchObject({ inputTokens: 100 })
    expect(updates.codexTurnId).toBe('turn-7')
    expect(updates.streamingType).toBe('plan')
  })

  it('deep-maps rate_limits tiers', () => {
    const updates = wireSessionInfoToUpdates({
      rate_limits: { five_hour: { status: 'allowed', utilization: 0.5 } },
    })
    expect(updates.rateLimits).toEqual({ five_hour: { status: 'allowed', utilization: 0.5 } })
  })

  it('only forwards a positive numeric thinking_tokens estimate', () => {
    expect(wireSessionInfoToUpdates({ thinking_tokens: 230 }).thinkingTokens).toBe(230)
    // 0 (the zero-estimate first delta), NaN, and non-numbers are all dropped,
    // so the indicator never has to defend against "0 tokens" or a NaN.
    expect('thinkingTokens' in wireSessionInfoToUpdates({ thinking_tokens: 0 })).toBe(false)
    expect('thinkingTokens' in wireSessionInfoToUpdates({ thinking_tokens: Number.NaN })).toBe(false)
    expect('thinkingTokens' in wireSessionInfoToUpdates({ thinking_tokens: '5' })).toBe(false)
  })

  it('skips keys that are absent or fail their type guard', () => {
    // A non-number cost and a context_usage with no token data contribute nothing.
    expect(wireSessionInfoToUpdates({ total_cost_usd: 'free', context_usage: {} })).toEqual({})
  })

  it('keeps an empty-string streaming_type (the "not streaming plan" signal)', () => {
    // streaming_type uses `!== undefined`, so "" is a meaningful value, not a skip.
    expect(wireSessionInfoToUpdates({ streaming_type: '' }).streamingType).toBe('')
  })
})
