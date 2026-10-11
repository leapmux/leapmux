import type { TurnEnd } from '../../../model/divider'
import { isObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'
import { unwrapACPResult } from '../resultWrapper'

/**
 * The stop reason that means the reader cancelled the turn. Every other reason the
 * protocol defines describes a turn that ran to an end of its own, so it qualifies the
 * shared "Turn ended" rather than replacing it.
 */
const ACP_STOP_CANCELLED = 'cancelled'
const ACP_STOP_END_TURN = 'end_turn'

/**
 * ACP result_divider model (turn completion). Null for a non-object message.
 *
 * The turn fields come through the shared unwrap, so this labels a native result
 * envelope and a flat answer the same way. The classifier reads `stopReason`
 * through that same unwrap, which keeps the two from disagreeing about which
 * object holds it.
 */
export function acpResultDivider(parsed: unknown): TurnEnd | null {
  const result = unwrapACPResult(parsed)
  if (!result)
    return null
  // pickString (not a raw cast) so a non-string stopReason degrades to '' rather
  // than coercing a number/object into the label; matches the other hooks.
  return acpStopReasonDivider(pickString(result, 'stopReason'))
}

/**
 * The divider of a turn that ended for `reason`, in the protocol's stop-reason words.
 *
 * A provider that ends a turn it started by itself with a frame of its own states
 * the same words there, so its divider reads the same as the divider of a prompt.
 */
export function acpStopReasonDivider(reason: string): TurnEnd {
  if (reason === ACP_STOP_CANCELLED)
    return { label: turnEndLabel('interrupted') }
  return { label: turnEndLabel('ended', { qualifiers: [reason !== ACP_STOP_END_TURN && reason] }) }
}

/**
 * The `extractDivider` hook of one provider.
 *
 * A provider that ends a turn it started by itself with a frame of its own reads
 * that frame's stop reason; every other frame is the protocol's prompt response.
 */
export function acpDividerReader(agentTurnEnd?: (parent: Record<string, unknown>) => string | undefined) {
  return (parsed: unknown): TurnEnd | null => {
    const reason = agentTurnEnd && isObject(parsed) ? agentTurnEnd(parsed) : undefined
    return reason !== undefined ? acpStopReasonDivider(reason) : acpResultDivider(parsed)
  }
}
