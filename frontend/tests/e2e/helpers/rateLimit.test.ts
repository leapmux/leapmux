import { describe, expect, it } from 'vitest'
import { rateLimitPopoverLabel } from '../../../src/lib/rateLimitUtils'
import { rateLimitMarkers, rateLimitWindowLabel } from './rateLimit'

describe('rateLimitWindowLabel', () => {
  it('reads the app label table for a window type the build knows', () => {
    expect(rateLimitWindowLabel('five_hour')).toBe(rateLimitPopoverLabel('five_hour'))
    expect(rateLimitWindowLabel('seven_day')).toBe(rateLimitPopoverLabel('seven_day'))
  })

  it('states the heading the card falls back to for a type the table does not carry', () => {
    expect(rateLimitWindowLabel('workspace_owner_credits_depleted'))
      .toBe('Rate Limit (workspace_owner_credits_depleted)')
  })

  it('states the bare heading when a window carries no type', () => {
    expect(rateLimitWindowLabel(undefined)).toBe('Rate Limit')
  })
})

describe('rateLimitMarkers', () => {
  // The marker block of the five-hour rate-limit specs: the heading and the
  // utilization the card prints as a whole-percent phrase.
  it('marks the window with its heading and its utilization phrase', () => {
    expect(rateLimitMarkers({
      type: 'five_hour',
      status: 'allowed_warning',
      utilization: 0.92,
      resetsAt: 1893456000,
    })).toEqual(['5-Hour Rate Limit', '92% used'])
  })

  it('marks the weekly window by its own heading', () => {
    expect(rateLimitMarkers({ type: 'seven_day', status: 'allowed_warning', utilization: 0.81 }))
      .toEqual(['7-Day Rate Limit', '81% used'])
  })

  it('skips the utilization the step did not script', () => {
    expect(rateLimitMarkers({ type: 'five_hour', status: 'allowed' }))
      .toEqual(['5-Hour Rate Limit'])
  })
})
