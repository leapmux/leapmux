import type { TurnEnd } from '../../../model/divider'
import { LETTA_DELTA_FIELD, LETTA_MESSAGE } from '~/generated/contracts/letta-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'
import { lettaErrorWords } from './notification'

/**
 * The turn-end divider of a Letta Code turn.
 *
 * Letta ends a turn with `turn_finished`, which states its `stop_reason`. The
 * Worker reads that word when it stores the turn end and records how the turn
 * ended: a stop that `abort_message` caused is an interrupted turn, and a model
 * request that failed is a failed turn. The divider reads that record, so it never
 * spells the stop words of one Letta Code release.
 *
 * An interruption is the reader's own choice, not a failure, so it draws in the
 * ordinary color, as every other provider's interrupted turn does.
 */
export function lettaResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  // The wire discriminator is `type`, not `kind`, on every protocol_v2
  // message. A `turn_finished` read from `kind` is not recognized and the turn
  // end draws nothing.
  if (!isObject(parsed) || pickString(parsed, 'type') !== LETTA_MESSAGE.TurnFinished)
    return null
  switch (completion) {
    case MessageCompletion.INTERRUPTED:
      return {
        label: turnEndLabel('interrupted'),
      }
    case MessageCompletion.ERROR: {
      const [head, ...lines] = lettaErrorWords(pickString(parsed, LETTA_DELTA_FIELD.Error)).split('\n')
      const detail = lines.join('\n').trim()
      return {
        label: `${turnEndLabel('failed')}${head
          ? ` — ${head}`
          : ''}`,
        isError: true,
        ...(detail
          ? { detail }
          : {}),
      }
    }
    default:
      return {
        label: turnEndLabel('ended'),
      }
  }
}
