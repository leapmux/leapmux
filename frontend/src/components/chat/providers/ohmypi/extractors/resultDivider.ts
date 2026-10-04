import type { TurnEnd } from '../../../model/divider'
import { OH_MY_PI_EVENT, OH_MY_PI_ROLE, OH_MY_PI_STOP_REASON } from '~/generated/contracts/ohmypi-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'

/** The last assistant message of a run, which states how the run ended. */
function lastAssistantMessage(messages: unknown): Record<string, unknown> | null {
  if (!Array.isArray(messages))
    return null
  for (let i = messages.length - 1; i >= 0; i--) {
    const message = messages[i]
    if (isObject(message) && pickString(message, 'role') === OH_MY_PI_ROLE.Assistant)
      return message
  }
  return null
}

/**
 * An omp `agent_end` read into the turn-end divider, or null for another frame.
 *
 * The run's last assistant message states how it ended in `stopReason`. The worker
 * measures the turn and adds `duration_ms`.
 *
 * `isTerminal: false` states that omp continues the work at once, and the frame does
 * not state why (`session/agent-session.ts`): a queued steer or follow-up, a waiting
 * agent-to-agent message, a retry, a compaction that continues the run. So the
 * divider states only that more work follows. A steer that LeapMux itself queued
 * draws no divider at all, because the worker keeps its turn open across that end.
 *
 * A turn that the READER stopped comes first, whatever the frame says: LeapMux knows
 * it sent the abort, and the completion records it.
 */
export function ohMyPiResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  if (!isObject(parsed) || pickString(parsed, 'type') !== OH_MY_PI_EVENT.AgentEnd)
    return null
  const durationMs = pickNumber(parsed, 'duration_ms')
  const continues = parsed.isTerminal === false && 'more work follows'
  if (completion === MessageCompletion.INTERRUPTED)
    return { label: turnEndLabel('interrupted', { durationMs }) }
  const assistant = lastAssistantMessage(parsed.messages)
  const stopReason = assistant ? pickString(assistant, 'stopReason') : ''
  if (stopReason === OH_MY_PI_STOP_REASON.Error) {
    return {
      label: turnEndLabel('failed', { durationMs, qualifiers: [continues], reason: pickString(assistant, 'errorMessage') }),
      isError: true,
    }
  }
  if (stopReason === OH_MY_PI_STOP_REASON.Aborted)
    return { label: turnEndLabel('interrupted', { durationMs, qualifiers: [continues] }) }
  return {
    label: turnEndLabel('ended', {
      durationMs,
      qualifiers: [stopReason === OH_MY_PI_STOP_REASON.Length && 'length limit', continues],
    }),
  }
}
