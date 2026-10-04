import type { TurnEnd } from '../../../model/divider'
import { LETTA_MESSAGE } from '~/generated/contracts/letta-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'

/**
 * The turn-end divider of a Letta Code turn.
 *
 * Letta ends a turn with `turn_finished`, which states its `stop_reason`. A turn
 * the reader stopped comes first: LeapMux knows it sent the abort, and the
 * completion records it.
 */
export function lettaResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  // The wire discriminator is `type`, not `kind`, on every protocol_v2
  // message. A `turn_finished` read from `kind` is not recognized and the turn
  // end draws nothing.
  if (!isObject(parsed) || (pickString(parsed, 'type') || pickString(parsed, 'kind')) !== LETTA_MESSAGE.TurnFinished)
    return null
  if (completion === MessageCompletion.INTERRUPTED)
    return { label: turnEndLabel('interrupted'), isError: true }
  const reason = pickString(parsed, 'stop_reason') || pickString(parsed, 'reason')
  return { label: turnEndLabel(reason === 'failed' ? 'failed' : 'ended') }
}
