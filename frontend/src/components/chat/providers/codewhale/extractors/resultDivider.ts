import type { TurnEnd } from '../../../model/divider'
import { CODEWHALE_EVENT, CODEWHALE_TURN_FIELD, CODEWHALE_TURN_STATUS } from '~/generated/contracts/codewhale-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'
import { codewhaleEnvelope } from './toolCommon'

/**
 * A Codewhale turn end as the provider-neutral divider model.
 *
 * `turn.completed` is the one event that ends a turn, and its turn record states how
 * the turn ended: `completed`, `failed`, `interrupted` or `canceled`. A failed turn
 * states its reason in `error`. Returns null for any other row, and for a status this
 * build does not know, so the caller draws the raw frame rather than a guess.
 */
export function codewhaleResultDivider(parsed: unknown): TurnEnd | null {
  const envelope = codewhaleEnvelope(parsed)
  if (!envelope || envelope.event !== CODEWHALE_EVENT.TurnCompleted)
    return null
  const turn = pickObject(envelope.payload, CODEWHALE_TURN_FIELD.Turn)
  const durationMs = pickNumber(turn, CODEWHALE_TURN_FIELD.DurationMs)
  switch (pickString(turn, CODEWHALE_TURN_FIELD.Status)) {
    case CODEWHALE_TURN_STATUS.Completed:
      return { label: turnEndLabel('ended', { durationMs }) }
    // The reader asked for both. `canceled` is a turn the runtime withdrew before it
    // ran, which is a stop rather than an error as well.
    case CODEWHALE_TURN_STATUS.Interrupted:
    case CODEWHALE_TURN_STATUS.Canceled:
      return { label: turnEndLabel('interrupted', { durationMs }) }
    case CODEWHALE_TURN_STATUS.Failed: {
      const error = pickString(turn, CODEWHALE_TURN_FIELD.Error).trim()
      // A long error states itself in the detail block, so a provider response body
      // does not stretch the rule.
      const [head = '', ...rest] = error.split('\n')
      const model: TurnEnd = { label: turnEndLabel('failed', { durationMs, reason: head }), isError: true }
      const detail = rest.join('\n').trim()
      return detail ? { ...model, detail } : model
    }
    default:
      return null
  }
}
