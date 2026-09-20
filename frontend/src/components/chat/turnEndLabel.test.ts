import { describe, expect, it } from 'vitest'
import { turnEndLabel } from './turnEndLabel'

describe('turnEndLabel', () => {
  it('states the outcome alone when the provider reports nothing else', () => {
    expect(turnEndLabel('ended')).toBe('Turn ended')
    expect(turnEndLabel('interrupted')).toBe('Turn interrupted')
    expect(turnEndLabel('failed')).toBe('Turn failed')
  })

  it('puts a duration in the parentheses', () => {
    expect(turnEndLabel('ended', { durationMs: 2000 })).toBe('Turn ended (2.0s)')
    expect(turnEndLabel('interrupted', { durationMs: 2600 })).toBe('Turn interrupted (2.6s)')
  })

  // A zero duration is a real measurement, so it reads as one. Only an absent
  // duration leaves the parentheses out.
  it('keeps a zero duration and drops an absent one', () => {
    expect(turnEndLabel('ended', { durationMs: 0 })).toBe('Turn ended (0ms)')
    expect(turnEndLabel('ended', { durationMs: null })).toBe('Turn ended')
    // An omitted duration is the absent form; the parts read it identically to null.
    expect(turnEndLabel('ended', {})).toBe('Turn ended')
  })

  it('lists the qualifiers after the duration and drops the empty ones', () => {
    expect(turnEndLabel('ended', { durationMs: 5900, qualifiers: ['auto-retry'] })).toBe('Turn ended (5.9s, auto-retry)')
    expect(turnEndLabel('ended', { qualifiers: ['max_tokens'] })).toBe('Turn ended (max_tokens)')
    expect(turnEndLabel('ended', { qualifiers: ['', false, null, undefined, 'length limit'] })).toBe('Turn ended (length limit)')
  })

  it('follows the outcome with the provider\'s own reason', () => {
    expect(turnEndLabel('failed', { reason: 'API Error: 529' })).toBe('Turn failed — API Error: 529')
    expect(turnEndLabel('failed', { durationMs: 3000, qualifiers: ['provider_error'], reason: 'the model refused' }))
      .toBe('Turn failed (3.0s, provider_error) — the model refused')
  })

  it('drops a reason that is empty or blank', () => {
    expect(turnEndLabel('failed', { reason: '' })).toBe('Turn failed')
    expect(turnEndLabel('failed', { reason: '   ' })).toBe('Turn failed')
  })
})
