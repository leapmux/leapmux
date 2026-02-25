import type { RateLimitInfo } from '~/stores/agentSession.store'

export const RATE_LIMIT_TYPE_LABELS: Record<string, string> = {
  five_hour: '5-hour',
  seven_day: '7-day',
}

/** Format seconds remaining as d:hh:mm or h:mm. Returns null if remaining time <= 0. */
export function formatCountdown(resetAtUnixSec: number): string | null {
  const remaining = resetAtUnixSec - Math.floor(Date.now() / 1000)
  if (remaining <= 0)
    return null
  const days = Math.floor(remaining / 86400)
  const hours = Math.floor((remaining % 86400) / 3600)
  const minutes = Math.floor((remaining % 3600) / 60)
  if (days > 0)
    return `${days}:${String(hours).padStart(2, '0')}:${String(minutes).padStart(2, '0')}`
  return `${hours}:${String(minutes).padStart(2, '0')}`
}

/** Get the applicable reset timestamp for a rate limit entry. */
export function getResetsAt(info: RateLimitInfo): number | undefined {
  return info.isUsingOverage ? info.overageResetsAt : info.resetsAt
}

/**
 * Pick the most important rate limit to display (exceeded > warning, then least remaining time).
 * Returns null if no rate limits are at warning or above.
 */
export function pickUrgentRateLimit(rateLimits: Record<string, RateLimitInfo>): { info: RateLimitInfo, countdown: string } | null {
  let best: RateLimitInfo | null = null
  let bestCountdown: string | null = null
  let bestSeverity = 0 // 0=none, 1=warning, 2=exceeded
  let bestRemaining = Infinity

  for (const info of Object.values(rateLimits)) {
    const status = info.status
    if (status === 'allowed' || !status)
      continue
    const severity = (status === 'allowed_warning') ? 1 : 2
    const resetsAt = getResetsAt(info)
    const remaining = resetsAt ? resetsAt - Math.floor(Date.now() / 1000) : Infinity
    const countdown = resetsAt ? formatCountdown(resetsAt) : null
    if (!countdown)
      continue

    if (severity > bestSeverity || (severity === bestSeverity && remaining < bestRemaining)) {
      best = info
      bestCountdown = countdown
      bestSeverity = severity
      bestRemaining = remaining
    }
  }
  return best && bestCountdown ? { info: best, countdown: bestCountdown } : null
}

/** Build a human-readable rate limit notification message. Defensive: all fields may be absent. */
export function formatRateLimitMessage(info: Record<string, unknown>): string {
  const status = info.status as string | undefined
  const rateLimitType = info.rateLimitType as string | undefined
  const utilization = info.utilization as number | undefined
  const isUsingOverage = info.isUsingOverage as boolean | undefined
  const resetsAt = (isUsingOverage ? info.overageResetsAt : info.resetsAt) as number | undefined

  const typeLabel = RATE_LIMIT_TYPE_LABELS[rateLimitType ?? ''] ?? rateLimitType ?? ''
  const prefix = typeLabel ? `${typeLabel} rate limit` : 'Rate limit'

  const parts: string[] = []
  if (status && status !== 'allowed' && status !== 'allowed_warning')
    parts.push('reached')
  if (typeof utilization === 'number')
    parts.push(`${Math.round(utilization * 100)}% used`)
  if (isUsingOverage)
    parts.push('overage')
  if (typeof resetsAt === 'number') {
    const countdown = formatCountdown(resetsAt)
    if (countdown)
      parts.push(`resets in ${countdown}`)
  }

  return parts.length > 0 ? `${prefix}: ${parts.join(' \u2014 ')}` : `${prefix} update`
}
