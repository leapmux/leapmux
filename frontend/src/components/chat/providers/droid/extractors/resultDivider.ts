import type { TurnEnd } from '../../../model/divider'
import { DROID_NOTIFICATION, DROID_NOTIFICATION_FIELD, DROID_TURN_END_REASON } from '~/generated/contracts/droid-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'

/**
 * The turn-end divider of a Factory Droid turn.
 *
 * Droid ends a turn with `agent_turn_completed`, which states its `reason`. The
 * reason `cancelled` is the end of a turn that `droid.interrupt_session` stopped, and
 * the Worker stores the frame as it arrived, so that word is the record of the stop.
 * The reason `error` is a turn that failed. Droid states more reasons, and each other
 * one reads as an end.
 *
 * A turn that LeapMux itself recorded as stopped comes first: LeapMux knows it sent
 * the abort, and the completion records it.
 */
export function droidResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  if (!isObject(parsed) || pickString(parsed, DROID_NOTIFICATION_FIELD.Type) !== DROID_NOTIFICATION.AgentTurnCompleted)
    return null
  const reason = pickString(parsed, DROID_NOTIFICATION_FIELD.Reason)
  if (completion === MessageCompletion.INTERRUPTED || reason === DROID_TURN_END_REASON.Cancelled)
    return { label: turnEndLabel('interrupted'), isError: true }
  if (reason === DROID_TURN_END_REASON.Error)
    return { label: turnEndLabel('failed'), isError: true }
  return { label: turnEndLabel('ended') }
}
