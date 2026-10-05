import type { MockModelRateLimits } from './mockModelScript'
import { describe, expect, it } from 'vitest'
import { claudeRateLimitHeaders } from './claudeSurface'
import { codexRateLimitHeaders } from './codexSurface'
import { copilotRateLimitHeaders } from './copilotSurface'
import { rateLimitHeaders } from './mockRateLimitHeaders'

describe('rateLimitHeaders', () => {
  it('sends nothing for a step without scripted quota or without a step', () => {
    expect(rateLimitHeaders(undefined)).toEqual({})
    expect(rateLimitHeaders({ text: 'No quota.' })).toEqual({})
  })

  it('states an allowed window with its reset and utilization, and every provider projection', () => {
    const rateLimits: MockModelRateLimits = { type: 'five_hour', status: 'allowed', utilization: 0.73, resetsAt: 1893456000 }
    const reset = new Date(1893456000 * 1000).toUTCString()
    expect(rateLimitHeaders({ text: 'Quota.', rateLimits })).toEqual({
      'x-ratelimit-limit-requests': '1000',
      'x-ratelimit-remaining-requests': '999',
      'x-ratelimit-limit-tokens': '1000000',
      'x-ratelimit-remaining-tokens': '999000',
      'x-ratelimit-reset-requests': reset,
      'x-ratelimit-reset-tokens': reset,
      'x-leapmux-e2e-ratelimit-type': 'five_hour',
      'x-leapmux-e2e-ratelimit-status': 'allowed',
      'x-leapmux-e2e-ratelimit-resets-at': '1893456000',
      'x-leapmux-e2e-ratelimit-utilization': '0.73',
      ...claudeRateLimitHeaders(rateLimits),
      ...codexRateLimitHeaders(rateLimits),
      ...copilotRateLimitHeaders(rateLimits),
    })
  })

  it('states an exhausted window with zero remaining and keeps a zero reset and utilization', () => {
    const headers = rateLimitHeaders({ text: 'Quota.', rateLimits: { type: 'weekly', status: 'exceeded', utilization: 0, resetsAt: 0 } })
    expect(headers).toMatchObject({
      'x-ratelimit-remaining-requests': '0',
      'x-ratelimit-remaining-tokens': '0',
      'x-ratelimit-reset-requests': new Date(0).toUTCString(),
      'x-leapmux-e2e-ratelimit-resets-at': '0',
      'x-leapmux-e2e-ratelimit-utilization': '0',
      'x-leapmux-e2e-ratelimit-status': 'exceeded',
    })
  })

  it('omits the reset and utilization headers that the script leaves absent', () => {
    const headers = rateLimitHeaders({ text: 'Quota.', rateLimits: { type: 'primary', status: 'allowed' } })
    for (const name of ['x-ratelimit-reset-requests', 'x-ratelimit-reset-tokens', 'x-leapmux-e2e-ratelimit-resets-at', 'x-leapmux-e2e-ratelimit-utilization'])
      expect(headers).not.toHaveProperty(name)
    expect(headers).toMatchObject({ 'x-leapmux-e2e-ratelimit-type': 'primary', 'x-ratelimit-remaining-requests': '999' })
  })
})
