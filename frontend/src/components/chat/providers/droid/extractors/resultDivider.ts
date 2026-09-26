import type { TurnEnd } from '../../../model/divider'
import { DROID_NOTIFICATION } from '~/generated/contracts/droid-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'

/**
 * The turn-end divider of a Factory Droid turn.
 *
 * Droid ends a turn with `agent_turn_completed`, which states its `reason`. A
 * turn the reader stopped comes first: LeapMux knows it sent the abort, and the
 * completion records it.
 */
export function droidResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  if (!isObject(parsed) || pickString(parsed, 'type') !== DROID_NOTIFICATION.AgentTurnCompleted)
    return null
  if (completion === MessageCompletion.INTERRUPTED)
    return { label: turnEndLabel('interrupted'), isError: true }
  const reason = pickString(parsed, 'reason')
  return { label: turnEndLabel(reason === 'failed' ? 'failed' : 'ended') }
}
