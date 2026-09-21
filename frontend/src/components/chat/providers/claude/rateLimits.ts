import type { ParsedMessageContent } from '~/lib/messageParser'
import type { RateLimitInfo, RateLimitUpdate } from '~/models/agentSession'
import { assignDefined, isObject, pickBoolean, pickNumber, pickString } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'

/**
 * Claude's `rate_limit_info` object, as the neutral usage shape.
 *
 * Claude Code spells the fields the store reads -- `status`, `rateLimitType`,
 * `utilization`, `resetsAt` and the overage trio -- so this reading takes them one
 * for one. It exists so the usage meter and the transcript row read the SAME object:
 * they took two separate casts before, and only one of them ever learned about the
 * overage fields.
 *
 * One CHECKED line per field, which is the shape `wireRateLimitsToCamel` uses on the
 * broadcast half of this same struct. The payload arrives off the wire, so a cast
 * promises a type nobody verified: a `utilization` that is a string reached the usage
 * meter as a string, where the percentage arithmetic yields NaN. Every picker's
 * fallback is an explicit `undefined`, so the field's type still comes from
 * `RateLimitInfo` and a mismatched picker fails to compile.
 *
 * `assignDefined` leaves a field the payload omits ABSENT rather than
 * present-and-undefined. That matters beyond tidiness: `agentSession.store` compares a
 * tier with `shallowEqual`, which reads key COUNTS first, so a form that wrote all
 * eight keys would compare unequal against the stored copy on every event.
 */
export function claudeRateLimitInfo(info: Record<string, unknown>): RateLimitInfo {
  const out: RateLimitInfo = {}
  assignDefined(out, 'rateLimitType', pickString(info, 'rateLimitType', undefined))
  assignDefined(out, 'status', pickString(info, 'status', undefined))
  assignDefined(out, 'utilization', pickNumber(info, 'utilization', undefined))
  assignDefined(out, 'resetsAt', pickNumber(info, 'resetsAt', undefined))
  assignDefined(out, 'surpassedThreshold', pickNumber(info, 'surpassedThreshold', undefined))
  assignDefined(out, 'overageStatus', pickString(info, 'overageStatus', undefined))
  assignDefined(out, 'overageResetsAt', pickNumber(info, 'overageResetsAt', undefined))
  assignDefined(out, 'isUsingOverage', pickBoolean(info, 'isUsingOverage', undefined))
  return out
}

/**
 * Claude raw rate_limit_event: `{type:"rate_limit_event", rate_limit_info:{...}}`.
 *
 * `isObject` refuses an ARRAY as well as a primitive, which is what a `typeof` test
 * accepts. An array carries none of the fields {@link claudeRateLimitInfo} reads, so
 * it produced a tier keyed `unknown` whose every field was absent -- a row on the
 * usage meter that states nothing. A payload that is no record now yields no tier.
 */
export function claudeRateLimitsFromMessage(parsed: ParsedMessageContent): RateLimitUpdate | null {
  const inner = getInnerMessage(parsed)
  if (!inner || inner.type !== 'rate_limit_event')
    return null
  const info = inner.rate_limit_info
  if (!isObject(info))
    return { mode: 'merge', values: {} }
  const key = pickString(info, 'rateLimitType') || 'unknown'
  return { mode: 'merge', values: { [key]: claudeRateLimitInfo(info) } }
}
