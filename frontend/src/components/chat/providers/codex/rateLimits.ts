import type { ParsedMessageContent } from '~/lib/messageParser'
import type { RateLimitInfo } from '~/models/agentSession'
import { CODEX_RATE_LIMIT_ACCOUNT_BLOCK_KEY, CODEX_RATE_LIMIT_REACHED_TIME_WINDOW } from '~/generated/contracts/worker-vocab'
import { pickObject } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'

// Codex's rate-limit wire vocabulary. It lives HERE, not in `~/lib/rateLimitUtils`:
// one provider's method name, tier keys, window-duration table and reached-type words
// are exactly the per-provider knowledge the shared layer must not carry. What STAYS
// shared there is the formatting of a `RateLimitInfo`, which every provider produces.

/** JSON-RPC method name for Codex rate-limit notifications. */
export const CODEX_RATE_LIMITS_METHOD = 'account/rateLimits/updated'

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
  const resetsAt = typeof tier.resetsAt === 'number' ? tier.resetsAt : undefined
  return {
    rateLimitType,
    utilization: usedPercent / 100,
    ...(resetsAt !== undefined ? { resetsAt } : {}),
    status: usedPercent >= 100 ? 'exceeded' : usedPercent >= 80 ? 'allowed_warning' : 'allowed',
  }
}

/**
 * Codex rate limits: {method:"account/rateLimits/updated", params:{rateLimits:{primary,secondary}}}.
 */
export function codexRateLimitsFromMessage(parsed: ParsedMessageContent): { key: string, info: RateLimitInfo }[] | null {
  const inner = getInnerMessage(parsed)
  if (!inner || inner.method !== CODEX_RATE_LIMITS_METHOD)
    return null
  const results: { key: string, info: RateLimitInfo }[] = []
  for (const { info } of iterCodexRateLimitTiers(inner)) {
    if (info.rateLimitType)
      results.push({ key: info.rateLimitType, info })
  }
  // Mirror the backend's elevate so this replay path agrees with the live session-info broadcast:
  // when the authoritative reached-type says a time-windowed limit is hit but rounding kept every
  // window under 100%, surface the most-utilized window as "exceeded".
  if (codexRateLimitReachedType(inner) === CODEX_RATE_LIMIT_REACHED_TIME_WINDOW
    && !results.some(r => r.info.status === 'exceeded')) {
    let top: { key: string, info: RateLimitInfo } | undefined
    for (const r of results) {
      if (!top || (r.info.utilization ?? 0) > (top.info.utilization ?? 0))
        top = r
    }
    if (top)
      top.info = { ...top.info, status: 'exceeded' }
  }
  const reachedType = codexRateLimitReachedType(inner)
  results.push({
    key: CODEX_RATE_LIMIT_ACCOUNT_BLOCK_KEY,
    info: reachedType !== undefined && reachedType !== CODEX_RATE_LIMIT_REACHED_TIME_WINDOW
      ? { rateLimitType: reachedType, status: 'exceeded' }
      : {},
  })
  return results
}
