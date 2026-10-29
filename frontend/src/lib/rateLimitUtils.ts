import type { RateLimitInfo } from '~/models/agentSession'
import { RATE_LIMIT_STATUS, RATE_LIMIT_TYPE } from '~/generated/contracts/session-info'
import { formatLocalDateTime } from '~/lib/dateFormat'

const RATE_LIMIT_TYPE_LABELS: Record<string, string> = {
  [RATE_LIMIT_TYPE.FiveHour]: '5-hour',
  [RATE_LIMIT_TYPE.SevenDay]: '7-day',
  [RATE_LIMIT_TYPE.OneMonth]: 'Monthly',
}

const RATE_LIMIT_POPOVER_LABELS: Record<string, string> = {
  [RATE_LIMIT_TYPE.FiveHour]: '5-Hour Rate Limit',
  [RATE_LIMIT_TYPE.SevenDay]: '7-Day Rate Limit',
  [RATE_LIMIT_TYPE.OneMonth]: 'Monthly Rate Limit',
}

const RATE_LIMIT_STATUS_LABELS: Record<string, string> = {
  [RATE_LIMIT_STATUS.Allowed]: 'Allowed',
  [RATE_LIMIT_STATUS.AllowedWarning]: 'Warning',
  [RATE_LIMIT_STATUS.Exceeded]: 'Exceeded',
}

/** Native block statuses outside the normalized vocabulary retain the exceeded label. */
export function rateLimitStatusLabel(status: string | undefined): string | undefined {
  if (!status)
    return undefined
  return Object.hasOwn(RATE_LIMIT_STATUS_LABELS, status)
    ? RATE_LIMIT_STATUS_LABELS[status]
    : 'Exceeded'
}

/**
 * The short words for a rate-limit type, or undefined for one this build does not
 * know.
 *
 * `Object.hasOwn`, not a bare index: providers feed `rateLimitType` unconstrained
 * wire text, and `"toString"` resolves to `Object.prototype.toString` -- a truthy
 * value, so the caller's own fallback never ran and the label rendered as the
 * function's source text.
 */
export function rateLimitTypeLabel(rateLimitType: string | undefined): string | undefined {
  const key = rateLimitType ?? ''
  return Object.hasOwn(RATE_LIMIT_TYPE_LABELS, key)
    ? RATE_LIMIT_TYPE_LABELS[key]
    : undefined
}

/** The popover heading for a rate-limit type, or undefined for one this build does not know. */
export function rateLimitPopoverLabel(rateLimitType: string | undefined): string | undefined {
  const key = rateLimitType ?? ''
  return Object.hasOwn(RATE_LIMIT_POPOVER_LABELS, key)
    ? RATE_LIMIT_POPOVER_LABELS[key]
    : undefined
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

/** Validate a native calendar date without assigning it a reset instant. */
export function parseCalendarResetDate(value: unknown): string | undefined {
  if (typeof value !== 'string')
    return undefined
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value)
  if (!match)
    return undefined
  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const leapYear = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const days = [
    31,
    leapYear ? 29 : 28,
    31,
    30,
    31,
    30,
    31,
    31,
    30,
    31,
    30,
    31,
  ][month - 1] ?? 0
  return year > 0 && day > 0 && day <= days
    ? value
    : undefined
}

/** An instant takes priority. A normal calendar reset does not describe overage. */
export function getResetsOn(info: RateLimitInfo): string | undefined {
  return info.isUsingOverage || getResetsAt(info) !== undefined
    ? undefined
    : parseCalendarResetDate(info.resetsOn)
}

/** Describe the native reset without converting a calendar date into a timestamp. */
export function rateLimitResetTooltip(info: RateLimitInfo): string | undefined {
  const resetsAt = getResetsAt(info)
  if (resetsAt !== undefined)
    return formatResetTimestamp(resetsAt)
  const resetsOn = getResetsOn(info)
  return resetsOn === undefined
    ? undefined
    : `Resets on ${resetsOn}`
}

/**
 * Pick the most important rate limit to display (exceeded > warning, then least remaining time).
 * Returns null if no rate limits are at warning or above.
 */
export function pickUrgentRateLimit(rateLimits: Record<string, RateLimitInfo>): { info: RateLimitInfo, countdown: string | null } | null {
  let best: RateLimitInfo | null = null
  let bestCountdown: string | null = null
  let bestSeverity = 0 // 0=none, 1=warning, 2=exceeded
  let bestRemaining = Infinity

  for (const info of Object.values(rateLimits)) {
    const status = info.status
    if (status === RATE_LIMIT_STATUS.Allowed || !status)
      continue
    const severity = (status === RATE_LIMIT_STATUS.AllowedWarning)
      ? 1
      : 2
    const resetsAt = getResetsAt(info)
    const remaining = resetsAt !== undefined
      ? resetsAt - Math.floor(Date.now() / 1000)
      : Infinity
    const countdown = resetsAt !== undefined
      ? formatCountdown(resetsAt)
      : null
    if (severity > bestSeverity || (severity === bestSeverity && remaining < bestRemaining)) {
      best = info
      bestCountdown = countdown
      bestSeverity = severity
      bestRemaining = remaining
    }
  }
  return best
    ? { info: best, countdown: bestCountdown }
    : null
}

/** Build a human-readable rate limit notification message. Defensive: all fields may be absent. */
export function formatRateLimitMessage(info: RateLimitInfo): string {
  const { status, rateLimitType, utilization, isUsingOverage } = info
  const resetsAt = isUsingOverage ? info.overageResetsAt : info.resetsAt

  const knownLabel = rateLimitTypeLabel(rateLimitType)
  const prefix = info.label?.trim() || (knownLabel
    ? `${knownLabel} rate limit`
    : rateLimitType
      ? `Rate limit (${rateLimitType})`
      : 'Rate limit')

  const parts: string[] = []
  if (status && status !== RATE_LIMIT_STATUS.Allowed && status !== RATE_LIMIT_STATUS.AllowedWarning)
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
  const resetsOn = getResetsOn(info)
  if (resetsOn !== undefined)
    parts.push(`resets on ${resetsOn}`)

  return parts.length > 0
    ? `${prefix}: ${parts.join(' \u2014 ')}`
    : `${prefix} update`
}

/** Format a reset timestamp as a human-readable local date string. */
export function formatResetTimestamp(unixSec: number): string {
  return `Resets at ${formatLocalDateTime(new Date(unixSec * 1000))}`
}

/** Build the status, utilization, overage, and calendar reset parts. */
export function rateLimitSummaryParts(info: RateLimitInfo): string[] {
  const status = info.status
  const exceeded = !!status && status !== RATE_LIMIT_STATUS.Allowed && status !== RATE_LIMIT_STATUS.AllowedWarning
  const parts: string[] = []

  // Status label
  const label = rateLimitStatusLabel(status)
  if (label)
    parts.push(label)

  // Utilization is redundant when the limit is exceeded.
  if (typeof info.utilization === 'number' && !exceeded)
    parts.push(`${Math.round(info.utilization * 100)}% used`)

  // Overage indicator
  if (info.isUsingOverage)
    parts.push('overage')

  const resetsOn = getResetsOn(info)
  if (resetsOn !== undefined)
    parts.push(`resets on ${resetsOn}`)
  return parts
}

/** Build a concise single-line summary for the popover card. */
export function formatRateLimitSummary(info: RateLimitInfo): string {
  const parts = rateLimitSummaryParts(info)
  const resetsAt = getResetsAt(info)
  // Reset countdown
  if (typeof resetsAt === 'number') {
    const countdown = formatCountdown(resetsAt)
    if (countdown)
      parts.push(`resets in ${countdown}`)
  }
  return parts.length > 0
    ? parts.join(', ')
    : 'Unknown'
}
