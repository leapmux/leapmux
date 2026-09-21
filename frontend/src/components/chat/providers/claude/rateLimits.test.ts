import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { claudeRateLimitInfo, claudeRateLimitsFromMessage } from './rateLimits'

/** Every field Claude spells in one `rate_limit_info` object. */
const FULL = {
  rateLimitType: 'five_hour',
  status: 'allowed_warning',
  utilization: 0.82,
  resetsAt: 1893456000,
  surpassedThreshold: 0.8,
  overageStatus: 'allowed',
  overageResetsAt: 1893460000,
  isUsingOverage: true,
}

/** The parsed message a `rate_limit_event` frame arrives as. */
function event(info: unknown): ParsedMessageContent {
  return {
    wrapper: null,
    topLevel: null,
    parentObject: { type: 'rate_limit_event', rate_limit_info: info },
    rawText: '',
  }
}

describe('claudeRateLimitInfo', () => {
  it('carries every field the event stated', () => {
    expect(claudeRateLimitInfo({ ...FULL })).toStrictEqual(FULL)
  })

  // A field the payload omits stays ABSENT rather than present-and-undefined:
  // `agentSession.store` compares a tier with `shallowEqual`, which reads key counts
  // first, so a form that wrote all eight keys compared unequal on every event.
  it('leaves a field the event omitted out of the keys it carries', () => {
    expect(Object.keys(claudeRateLimitInfo({ status: 'exceeded' })).sort()).toStrictEqual(['status'])
    expect(Object.keys(claudeRateLimitInfo({})).sort()).toStrictEqual([])
  })

  // The reading is a NARROWING now rather than an assertion. A `utilization` that
  // arrives as a string reached the usage meter as a string, where the percentage
  // arithmetic yields NaN.
  it('drops a field whose value is of the wrong type', () => {
    const wrong = {
      rateLimitType: 42,
      status: { word: 'exceeded' },
      utilization: '0.9',
      resetsAt: '1893456000',
      surpassedThreshold: null,
      overageStatus: 7,
      overageResetsAt: [],
      isUsingOverage: 'true',
    }
    expect(claudeRateLimitInfo(wrong)).toStrictEqual({})
    expect(Object.keys(claudeRateLimitInfo(wrong)).sort()).toStrictEqual([])
  })

  it('keeps a real zero, which is a measurement rather than a missing one', () => {
    expect(claudeRateLimitInfo({ utilization: 0, resetsAt: 0, isUsingOverage: false }))
      .toStrictEqual({ utilization: 0, resetsAt: 0, isUsingOverage: false })
  })
})

describe('claudeRateLimitsFromMessage', () => {
  it('answers null for a frame that is no rate-limit event', () => {
    const assistant: ParsedMessageContent = { wrapper: null, topLevel: null, parentObject: { type: 'assistant' }, rawText: '' }
    expect(claudeRateLimitsFromMessage(assistant)).toBeNull()
  })

  it('keys the tier by the type the event stated', () => {
    expect(claudeRateLimitsFromMessage(event({ ...FULL }))).toStrictEqual({
      mode: 'merge',
      values: { five_hour: FULL },
    })
  })

  it('keys a tier that states no type as unknown', () => {
    expect(claudeRateLimitsFromMessage(event({ status: 'exceeded' })))
      .toStrictEqual({ mode: 'merge', values: { unknown: { status: 'exceeded' } } })
  })

  it('answers an empty merge for an event whose info is no object', () => {
    expect(claudeRateLimitsFromMessage(event('exceeded'))).toStrictEqual({ mode: 'merge', values: {} })
    expect(claudeRateLimitsFromMessage(event(null))).toStrictEqual({ mode: 'merge', values: {} })
  })

  // An array passes a `typeof info === 'object'` test and carries none of the fields
  // the reader picks, so it used to yield one tier keyed `unknown` with an empty info
  // -- a row on the usage meter that states nothing. A record is what the reader needs.
  it('answers an empty merge for an event whose info is an array', () => {
    expect(claudeRateLimitsFromMessage(event([]))).toStrictEqual({ mode: 'merge', values: {} })
    expect(claudeRateLimitsFromMessage(event([{ ...FULL }]))).toStrictEqual({ mode: 'merge', values: {} })
  })
})
