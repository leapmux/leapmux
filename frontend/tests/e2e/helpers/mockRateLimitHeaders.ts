import type { MockModelStep } from './mockModelScript'
import { claudeRateLimitHeaders } from './claudeSurface'
import { codexRateLimitHeaders } from './codexSurface'
import { copilotRateLimitHeaders } from './copilotSurface'

/**
 * Compose standard quota headers with the provider-owned native projections.
 *
 * Every model route of the mock sends the same headers: the generic routes of
 * the mock server and the Google route alike. Each native client reads the
 * shape it knows and ignores the rest.
 * The x-leapmux-e2e-* fields retain the scripted quota vocabulary for assertions.
 * Each Surface owns the header shapes that its native client reads.
 */
export function rateLimitHeaders(step: MockModelStep | undefined): Record<string, string> {
  const rateLimits = step?.rateLimits
  if (!rateLimits)
    return {}
  const allowed = rateLimits.status === 'allowed'
  const headers: Record<string, string> = {
    'x-ratelimit-limit-requests': '1000',
    'x-ratelimit-remaining-requests': allowed ? '999' : '0',
    'x-ratelimit-limit-tokens': '1000000',
    'x-ratelimit-remaining-tokens': allowed ? '999000' : '0',
    'x-leapmux-e2e-ratelimit-type': rateLimits.type,
    'x-leapmux-e2e-ratelimit-status': rateLimits.status,
  }
  if (rateLimits.resetsAt !== undefined) {
    const resetHttp = new Date(rateLimits.resetsAt * 1000).toUTCString()
    headers['x-ratelimit-reset-requests'] = resetHttp
    headers['x-ratelimit-reset-tokens'] = resetHttp
    headers['x-leapmux-e2e-ratelimit-resets-at'] = String(rateLimits.resetsAt)
  }
  if (rateLimits.utilization !== undefined)
    headers['x-leapmux-e2e-ratelimit-utilization'] = String(rateLimits.utilization)
  Object.assign(headers, claudeRateLimitHeaders(rateLimits), codexRateLimitHeaders(rateLimits), copilotRateLimitHeaders(rateLimits))
  return headers
}
