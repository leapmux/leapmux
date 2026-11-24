import type { TurnEnd } from '../../../model/divider'
import { KIMI_CONVERSATION_FIELD, KIMI_CONVERSATION_OUTCOME, KIMI_CONVERSATION_STATUS, KIMI_CONVERSATION_TURN_KIND } from '~/generated/contracts/kimi-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'
import { kimiConversationRow, kimiTurnIsBlocked } from '../conversation'

/** Read a completed, cancelled or failed native turn into the shared divider. */
export function kimiResultDivider(parsed: unknown): TurnEnd | null {
  const conversation = kimiConversationRow(parsed)
  if (conversation?.kind === 'turn' && (conversation.turn[KIMI_CONVERSATION_FIELD.Kind] !== KIMI_CONVERSATION_TURN_KIND.Agent || kimiTurnIsBlocked(conversation.turn)))
    return null
  if (conversation?.kind === 'turn' && conversation.turn[KIMI_CONVERSATION_FIELD.Status] === KIMI_CONVERSATION_STATUS.Ended) {
    const durationMs = pickNumber(conversation.turn, KIMI_CONVERSATION_FIELD.DurationMS)
    switch (conversation.turn[KIMI_CONVERSATION_FIELD.Outcome]) {
      case KIMI_CONVERSATION_OUTCOME.Completed:
        return {
          label: turnEndLabel('ended', { durationMs }),
        }
      case KIMI_CONVERSATION_OUTCOME.Interrupted:
        return {
          label: turnEndLabel('interrupted', { durationMs }),
        }
      default:
        return {
          label: turnEndLabel('failed', {
            durationMs,
            reason: pickString(pickObject(conversation.turn, KIMI_CONVERSATION_FIELD.Error), 'message'),
          }),
          isError: true,
        }
    }
  }
  return null
}
