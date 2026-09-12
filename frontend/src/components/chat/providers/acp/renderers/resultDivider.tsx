import type { ResultDividerModel } from '../../registry'
import { isObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'

/**
 * The stop reason that means the reader cancelled the turn. Every other reason the
 * protocol defines describes a turn that ran to an end of its own, so it qualifies the
 * shared "Turn ended" rather than replacing it.
 */
const ACP_STOP_CANCELLED = 'cancelled'
const ACP_STOP_END_TURN = 'end_turn'

/** ACP result_divider model (turn completion). Null for a non-object message. */
export function acpResultDivider(parsed: unknown): ResultDividerModel | null {
  if (!isObject(parsed))
    return null
  // pickString (not a raw cast) so a non-string stopReason degrades to '' rather
  // than coercing a number/object into the label; matches the other hooks.
  const reason = pickString(parsed, 'stopReason')
  if (reason === ACP_STOP_CANCELLED)
    return { label: turnEndLabel('interrupted') }
  return { label: turnEndLabel('ended', { qualifiers: [reason !== ACP_STOP_END_TURN && reason] }) }
}
