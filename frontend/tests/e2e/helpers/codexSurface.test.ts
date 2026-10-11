import { describe, expect, it } from 'vitest'
import { codexRateLimitHeaders } from './codexSurface'

describe('codexRateLimitHeaders', () => {
  it.each([
    { type: 'five_hour', prefix: 'primary', minutes: '300' },
    { type: 'seven_day', prefix: 'secondary', minutes: '10080' },
    { type: 'seven_day_sonnet', prefix: 'secondary', minutes: '10080' },
    { type: 'weekly', prefix: 'primary', minutes: '300' },
    { type: '', prefix: 'primary', minutes: '300' },
  ])('keeps the selected native window for $type', ({ type, prefix, minutes }) => {
    expect(codexRateLimitHeaders({ type, status: 'allowed' })).toEqual({ [`x-codex-${prefix}-window-minutes`]: minutes, 'x-codex-limit-name': type })
  })

  it.each([0, 0.5, 1])('converts utilization %s to the native percent and retains the zero reset', (utilization) => {
    expect(codexRateLimitHeaders({ type: 'five_hour', status: 'allowed', utilization, resetsAt: 0 })).toEqual({
      'x-codex-primary-window-minutes': '300',
      'x-codex-limit-name': 'five_hour',
      'x-codex-primary-used-percent': String(utilization * 100),
      'x-codex-primary-reset-at': '0',
    })
  })

  it.each(['exceeded', 'rate_limited'])('keeps the native reached type for %s', (status) => {
    expect(codexRateLimitHeaders({ type: 'seven_day', status })).toHaveProperty('x-codex-rate-limit-reached-type', 'rate_limit_reached')
  })

  it.each(['allowed', 'allowed_warning', 'rejected', ''])('does not add the reached type for %j', (status) => {
    expect(codexRateLimitHeaders({ type: 'five_hour', status })).not.toHaveProperty('x-codex-rate-limit-reached-type')
  })

  it('keeps large reset values and does not mutate the source record', () => {
    const limits = Object.freeze({ type: 'seven_day', status: 'allowed', resetsAt: Number.MAX_SAFE_INTEGER })
    expect(codexRateLimitHeaders(limits)).toHaveProperty('x-codex-secondary-reset-at', String(Number.MAX_SAFE_INTEGER))
    expect(limits).toEqual({ type: 'seven_day', status: 'allowed', resetsAt: Number.MAX_SAFE_INTEGER })
  })
})
