import type { TurnEnd } from '../../../model/divider'
import { PI_EVENT, PI_ROLE, PI_STOP_REASON } from '~/generated/contracts/pi-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'

function lastAssistantMessage(messages: unknown): Record<string, unknown> | null {
  if (!Array.isArray(messages))
    return null
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i]
    if (isObject(msg) && pickString(msg, 'role') === PI_ROLE.Assistant)
      return msg
  }
  return null
}

/**
 * Read a Pi agent_end into a turn divider from its last assistant message.
 * Return null for every other event.
 *
 * Pi reports no duration. The Worker supplies duration_ms.
 * An absent duration shows no time. A zero duration shows "(0ms)".
 *
 * The Worker stores the final run only after the native settlement.
 *
 * Worker completion takes precedence when the reader stops the turn.
 * A clean stop reports "aborted". A stop during a tool can report "error"
 * and "This operation was aborted". Pi uses the same frame shape for a failure.
 * The Worker records its requested stop in completion to distinguish these outcomes.
 * Without completion, that stop shows "Turn failed" in the error color.
 */
export function piResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  if (!isObject(parsed) || pickString(parsed, 'type') !== PI_EVENT.AgentEnd)
    return null

  if (completion === MessageCompletion.INTERRUPTED) {
    return {
      label: turnEndLabel('interrupted', {
        durationMs: pickNumber(parsed, 'duration_ms'),
      }),
    }
  }

  const assistant = lastAssistantMessage(parsed.messages)
  const stopReason = assistant
    ? pickString(assistant, 'stopReason')
    : ''
  const errorMessage = assistant
    ? pickString(assistant, 'errorMessage')
    : ''

  const durationMs = pickNumber(parsed, 'duration_ms')

  if (stopReason === PI_STOP_REASON.Error) {
    return {
      label: turnEndLabel('failed', { durationMs, reason: errorMessage }),
      isError: true,
    }
  }
  // An aborted turn is not an error: the reader asked for it.
  if (stopReason === PI_STOP_REASON.Aborted) {
    return {
      label: turnEndLabel('interrupted', { durationMs }),
    }
  }
  return {
    label: turnEndLabel('ended', {
      durationMs,
      qualifiers: [stopReason === PI_STOP_REASON.Length && 'length limit'],
    }),
  }
}
