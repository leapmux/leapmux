import type { RateLimitInfo } from '~/stores/agentSession.store'
import { formatLocalDateTime } from '~/lib/dateFormat'
import { pickObject } from '~/lib/jsonPick'

/** JSON-RPC method name for Codex rate-limit notifications. */
export const CODEX_RATE_LIMITS_METHOD = 'account/rateLimits/updated'

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

/** Codex rate-limit tier keys, ordered from most-restrictive to least-restrictive. */
const CODEX_RATE_LIMIT_TIER_KEYS = ['primary', 'secondary'] as const
export type CodexRateLimitTierKey = typeof CODEX_RATE_LIMIT_TIER_KEYS[number]

export interface CodexRateLimitTierEntry {
  key: CodexRateLimitTierKey
  tier: Record<string, unknown>
  info: RateLimitInfo
}

/**
 * Walk the `params.rateLimits.{primary,secondary}` tiers of a Codex
 * `account/rateLimits/updated` payload, yielding the parsed `RateLimitInfo`
 * for each tier that's present. Skips tiers whose payload is missing or
 * not an object. Used by both the notification renderer and the all-allowed
 * predicate so the tier-walking shape lives in one place.
 */
export function* iterCodexRateLimitTiers(payload: Record<string, unknown> | null | undefined): Generator<CodexRateLimitTierEntry> {
  const rl = pickObject(pickObject(payload, 'params'), 'rateLimits')
  if (!rl)
    return
  for (const key of CODEX_RATE_LIMIT_TIER_KEYS) {
    const tier = pickObject(rl, key)
    if (!tier)
      continue
    yield { key, tier, info: codexTierToRateLimitInfo(tier) }
  }
}

/**
 * The one Codex `rateLimitReachedType` that lifts on the rolling-window timer
 * (the others are billing/usage caps). Mirrors the backend constant of the same
 * name; used to elevate a rounded-under-100 window to "exceeded".
 */
export const CODEX_RATE_LIMIT_REACHED_TIME_WINDOW = 'rate_limit_reached'

/**
 * Codex `rateLimitReachedType` values (snake_case, from the v2 RateLimitSnapshot)
 * mapped to display labels. `rate_limit_reached` is the time-windowed limit that
 * lifts on its own; the others are billing/usage caps that a reset timer won't
 * clear. Newer Codex builds emit this snapshot-level field; older builds omit it.
 */
export const CODEX_RATE_LIMIT_REACHED_LABELS: Record<string, string> = {
  rate_limit_reached: 'Rate limit reached',
  workspace_owner_credits_depleted: 'Out of credits',
  workspace_member_credits_depleted: 'Out of credits',
  workspace_owner_usage_limit_reached: 'Usage limit reached',
  workspace_member_usage_limit_reached: 'Usage limit reached',
}

/**
 * Read the snapshot-level `rateLimitReachedType` from a Codex
 * `account/rateLimits/updated` payload. This is Codex's authoritative
 * "an actual limit was hit" signal -- present even when no rolling window is over
 * its threshold (e.g. credit depletion) -- so it must be surfaced independently
 * of the per-tier usedPercent classification. Returns undefined when absent or
 * empty (older Codex builds, or a routine non-blocking update).
 */
export function codexRateLimitReachedType(payload: Record<string, unknown> | null | undefined): string | undefined {
  const rl = pickObject(pickObject(payload, 'params'), 'rateLimits')
  const t = rl?.rateLimitReachedType
  return typeof t === 'string' && t.length > 0 ? t : undefined
}

/** Human-readable label for a Codex rateLimitReachedType, with a generic fallback. */
export function formatCodexRateLimitReached(reachedType: string): string {
  return CODEX_RATE_LIMIT_REACHED_LABELS[reachedType] ?? 'Rate limit reached'
}

/** Convert a Codex rate limit tier to RateLimitInfo. */
export function codexTierToRateLimitInfo(tier: Record<string, unknown>): RateLimitInfo {
  // `tier` is wire-shaped `Record<string, unknown>`, so coerce defensively rather than
  // `as number`: a non-numeric usedPercent (a malformed/replayed payload) would otherwise
  // produce a NaN utilization and an 'allowed' status that disagrees with the backend's
  // typed float64 classification. A missing/non-numeric windowDurationMins falls back to
  // an empty type key (no `NaN_hour`).
  const usedPercent = typeof tier.usedPercent === 'number' ? tier.usedPercent : 0
  const windowMins = typeof tier.windowDurationMins === 'number' ? tier.windowDurationMins : undefined
  const rateLimitType = windowMins === undefined
    ? ''
    : WINDOW_DURATION_TYPES[windowMins]
      ?? (windowMins >= 1440 ? `${Math.round(windowMins / 1440)}_day` : `${Math.round(windowMins / 60)}_hour`)
  return {
    rateLimitType,
    utilization: usedPercent / 100,
    resetsAt: typeof tier.resetsAt === 'number' ? tier.resetsAt : undefined,
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
