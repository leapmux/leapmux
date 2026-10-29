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
 * A process exit interrupts the turn also. Permission refusals and model failures report a failed turn.
 * Completed work and a specification handoff report a successful end.
 *
 * A turn that LeapMux itself recorded as stopped comes first: LeapMux knows it sent
 * the abort, and the completion records it.
 */
export function droidResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  if (!isObject(parsed) || pickString(parsed, DROID_NOTIFICATION_FIELD.Type) !== DROID_NOTIFICATION.AgentTurnCompleted)
    return null
  const reason = pickString(parsed, DROID_NOTIFICATION_FIELD.Reason)
  if (completion === MessageCompletion.INTERRUPTED || reason === DROID_TURN_END_REASON.Cancelled || reason === DROID_TURN_END_REASON.ProcessExit) {
    return {
      label: turnEndLabel('interrupted'),
      isError: true,
    }
  }
  if (completion === MessageCompletion.ERROR) {
    return {
      label: turnEndLabel('failed'),
      isError: true,
    }
  }
  switch (reason) {
    case '':
    case DROID_TURN_END_REASON.Completed:
    case DROID_TURN_END_REASON.SpecHandoff:
      return {
        label: turnEndLabel('ended'),
      }
    case DROID_TURN_END_REASON.Error:
    case DROID_TURN_END_REASON.PermissionRejected:
    case DROID_TURN_END_REASON.StructuredOutputMissing:
    case DROID_TURN_END_REASON.StructuredOutputInvalid:
    case DROID_TURN_END_REASON.StructuredOutputSchemaInvalid:
    case DROID_TURN_END_REASON.ModelUsageExhausted:
    case DROID_TURN_END_REASON.ModelAuthenticationFailed:
    case DROID_TURN_END_REASON.ModelRequestRejected:
    case DROID_TURN_END_REASON.ModelProviderUnreachable:
    case DROID_TURN_END_REASON.ModelProviderUnavailable:
    case DROID_TURN_END_REASON.ModelRateLimited:
    case DROID_TURN_END_REASON.PromptRejected:
    case DROID_TURN_END_REASON.CompletionPersistenceFailed:
    case DROID_TURN_END_REASON.NoApproverAvailable:
    default:
      return {
        label: turnEndLabel('failed'),
        isError: true,
      }
  }
}
