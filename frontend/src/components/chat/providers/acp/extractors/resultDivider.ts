import type { DividerIR } from '../../../ir/divider'
import { pickString } from '~/lib/jsonPick'
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
export function acpResultDivider(parsed: unknown): DividerIR | null {
  const result = unwrapACPResult(parsed)
  if (!result)
    return null
  // pickString (not a raw cast) so a non-string stopReason degrades to '' rather
  // than coercing a number/object into the label; matches the other hooks.
  const reason = pickString(result, 'stopReason')
  if (reason === ACP_STOP_CANCELLED)
    return { label: turnEndLabel('interrupted') }
  return { label: turnEndLabel('ended', { qualifiers: [reason !== ACP_STOP_END_TURN && reason] }) }
}
