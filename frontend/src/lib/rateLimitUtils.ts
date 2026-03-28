import type { RateLimitInfo } from '~/stores/agentSession.store'
import { formatLocalDateTime } from '~/lib/dateFormat'

export const RATE_LIMIT_TYPE_LABELS: Record<string, string> = {
  five_hour: '5-hour',
  seven_day: '7-day',
}

export const RATE_LIMIT_POPOVER_LABELS: Record<string, string> = {
  five_hour: '5-Hour Rate Limit',
  seven_day: '7-Day Rate Limit',
}

/** Window-duration-to-type mapping for Codex rate limits. */
const WINDOW_DURATION_TYPES: Record<number, string> = { 300: 'five_hour', 10080: 'seven_day' }

/** Convert a Codex rate limit tier to RateLimitInfo. */
export function codexTierToRateLimitInfo(tier: Record<string, unknown>): RateLimitInfo {
  const usedPercent = tier.usedPercent as number ?? 0
  const windowMins = tier.windowDurationMins as number
  const rateLimitType = WINDOW_DURATION_TYPES[windowMins]
    ?? (windowMins >= 1440 ? `${Math.round(windowMins / 1440)}_day` : `${Math.round(windowMins / 60)}_hour`)
  return {
    rateLimitType,
    utilization: usedPercent / 100,
    resetsAt: tier.resetsAt as number | undefined,
    status: usedPercent >= 100 ? 'exceeded' : usedPercent >= 80 ? 'allowed_warning' : 'allowed',
  }
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
export function formatRateLimitMessage(info: RateLimitInfo): string {
  const { status, rateLimitType, utilization, isUsingOverage } = info
  const resetsAt = isUsingOverage ? info.overageResetsAt : info.resetsAt

  const knownLabel = RATE_LIMIT_TYPE_LABELS[rateLimitType ?? '']
  const prefix = knownLabel
    ? `${knownLabel} rate limit`
    : rateLimitType ? `Rate limit (${rateLimitType})` : 'Rate limit'

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

/** Format a reset timestamp as a human-readable local date string. */
export function formatResetTimestamp(unixSec: number): string {
  return `Resets at ${formatLocalDateTime(new Date(unixSec * 1000))}`
}

/** Build a concise single-line summary for the popover card. */
export function formatRateLimitSummary(info: RateLimitInfo): string {
  const status = info.status
  const exceeded = !!status && status !== 'allowed' && status !== 'allowed_warning'
  const resetsAt = getResetsAt(info)

  const parts: string[] = []

  // Status label
  if (status === 'allowed')
    parts.push('Allowed')
  else if (status === 'allowed_warning')
    parts.push('Warning')
  else if (exceeded)
    parts.push('Exceeded')

  // Utilization — skip when exceeded (redundant)
  if (typeof info.utilization === 'number' && !exceeded)
    parts.push(`${Math.round(info.utilization * 100)}% used`)

  // Overage indicator
  if (info.isUsingOverage)
    parts.push('overage')

  // Reset countdown
  if (typeof resetsAt === 'number') {
    const countdown = formatCountdown(resetsAt)
    if (countdown)
      parts.push(`resets in ${countdown}`)
  }

  return parts.length > 0 ? parts.join(', ') : 'Unknown'
}
