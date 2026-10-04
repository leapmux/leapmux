import type { TurnEnd } from '../../../model/divider'
import { KIMI_EVENT, KIMI_TURN_END } from '~/generated/contracts/kimi-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'
import { kimiEventData } from '../protocol'

/**
 * A Kimi Code `turn.ended` as the provider-neutral divider.
 *
 * `reason` is one of four words: `completed`, `cancelled`, `failed` and `blocked`. A
 * failed turn carries the error it failed with, and a blocked one states that a hook or
 * a policy stopped it. Returns null for any other row, so the caller draws the shared
 * unrecognized card.
 */
export function kimiResultDivider(parsed: unknown): TurnEnd | null {
  const data = kimiEventData(parsed, KIMI_EVENT.TurnEnded)
  if (!data)
    return null
  const durationMs = pickNumber(data, 'durationMs')
  switch (pickString(data, 'reason')) {
    case KIMI_TURN_END.Completed:
      return { label: turnEndLabel('ended', { durationMs }) }
    case KIMI_TURN_END.Cancelled:
      return { label: turnEndLabel('interrupted', { durationMs }) }
    case KIMI_TURN_END.Blocked:
      return { label: turnEndLabel('failed', { durationMs, reason: 'blocked by a hook or a policy' }), isError: true }
    case KIMI_TURN_END.Failed:
    default: {
      // A reason this build does not know reads as a failure: the turn did not state
      // that it completed.
      const error = pickObject(data, 'error')
      const code = pickString(error, 'code')
      return {
        label: turnEndLabel('failed', { durationMs, qualifiers: [code], reason: pickString(error, 'message') }),
        isError: true,
      }
    }
  }
}
