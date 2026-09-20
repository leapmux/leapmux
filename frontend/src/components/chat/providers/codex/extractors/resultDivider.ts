import type { TurnEnd } from '../../../model/divider'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'
import { CODEX_STATUS } from '../itemVocabulary'

/**
 * The statuses that mean the reader stopped the turn. `CODEX_STATUS` holds the three
 * a TOOL call reports; a turn also ends on one of these, which no tool status covers.
 */
const CODEX_TURN_INTERRUPTED = new Set<string>(['interrupted', 'cancelled', 'aborted'])

/**
 * Codex `turn/completed` → result_divider model. A failed turn with an error
 * object renders the message (and `additionalDetails` inline) in danger color;
 * any other status renders the shared turn-end label. Returns null when the turn
 * carries no status; classify only routes a status-bearing turn here, so the
 * null branch is a defensive guard rather than a routine "render nothing" path.
 *
 * A status this build does not recognize qualifies the shared "Turn ended" rather
 * than becoming the label, so a word the runtime adds later still reads as a turn end.
 */
export function codexResultDivider(parsed: unknown): TurnEnd | null {
  if (!isObject(parsed))
    return null
  const turn = pickObject(parsed, 'turn')
  const status = pickString(turn, 'status')
  if (!status)
    return null

  const error = pickObject(turn, 'error')
  if (status === CODEX_STATUS.FAILED && error) {
    // `|| 'Unknown error'` (not just pickString's missing-key fallback) so an
    // explicit empty-string message doesn't render a label-less red divider --
    // pickString treats '' as a present string and would otherwise pass it through.
    const message = pickString(error, 'message') || 'Unknown error'
    const details = pickString(error, 'additionalDetails')
    // `additionalDetails` goes in the detail block rather than the label: the label
    // already carries one em dash, and a second one reads as a list of two reasons.
    const model: TurnEnd = { label: turnEndLabel('failed', { reason: message }), isError: true }
    if (details)
      model.detail = details
    return model
  }
  if (status === CODEX_STATUS.FAILED)
    return { label: turnEndLabel('failed'), isError: true }
  if (CODEX_TURN_INTERRUPTED.has(status))
    return { label: turnEndLabel('interrupted') }
  return { label: turnEndLabel('ended', { qualifiers: [status !== CODEX_STATUS.COMPLETED && status] }) }
}
