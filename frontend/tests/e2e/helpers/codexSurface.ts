import type { MockModelRateLimits } from './mockModelScript'

/**
 * Write the x-codex-* headers for one scripted quota window.
 *
 * This mock uses primary for a five-hour window and secondary for a seven-day window.
 * Codex reads the supplied window duration. It does not infer duration from the prefix.
 * used-percent ranges from 0 to 100. The script's utilization ranges from 0 to 1.
 * Write only the window that type selects, so Codex reports one window.
 */
export function codexRateLimitHeaders(rateLimits: MockModelRateLimits): Record<string, string> {
  const window = rateLimits.type.startsWith('seven_day') ? 'secondary' : 'primary'
  const minutes = rateLimits.type.startsWith('seven_day') ? '10080' : '300'
  const headers: Record<string, string> = {
    [`x-codex-${window}-window-minutes`]: minutes,
    'x-codex-limit-name': rateLimits.type,
  }
  if (rateLimits.utilization !== undefined)
    headers[`x-codex-${window}-used-percent`] = String(rateLimits.utilization * 100)
  if (rateLimits.resetsAt !== undefined)
    headers[`x-codex-${window}-reset-at`] = String(rateLimits.resetsAt)
  if (rateLimits.status === 'exceeded' || rateLimits.status === 'rate_limited')
    headers['x-codex-rate-limit-reached-type'] = 'rate_limit_reached'
  return headers
}
