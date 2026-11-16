import type { TurnEnd } from '../../../model/divider'
import { KIMI_TRANSCRIPT_TURN } from '~/generated/contracts/kimi-protocol'
import { pickNumber, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'
import { kimiTranscriptTurn } from '../transcript'

/** Read a completed, cancelled or failed native turn into the shared divider. */
export function kimiResultDivider(parsed: unknown): TurnEnd | null {
  const native = kimiTranscriptTurn(parsed)
  if (native) {
    const durationMs = pickNumber(native, 'durationMs')
    switch (pickString(native, 'state')) {
      case KIMI_TRANSCRIPT_TURN.Completed:
        return {
          label: turnEndLabel('ended', { durationMs }),
        }
      case KIMI_TRANSCRIPT_TURN.Cancelled:
        return {
          label: turnEndLabel('interrupted', { durationMs }),
        }
      case KIMI_TRANSCRIPT_TURN.Failed:
        return {
          label: turnEndLabel('failed', {
            durationMs,
            reason: pickString(native, 'error'),
          }),
          isError: true,
        }
      default:
        return null
    }
  }
  return null
}
