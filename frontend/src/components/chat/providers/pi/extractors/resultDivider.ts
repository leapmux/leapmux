import type { DividerIR } from '../../../ir/divider'
import { PI_EVENT } from '~/generated/contracts/pi-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickBool, pickNumber, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'

function lastAssistantMessage(messages: unknown): Record<string, unknown> | null {
  if (!Array.isArray(messages))
    return null
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i]
    if (isObject(msg) && pickString(msg, 'role') === 'assistant')
      return msg
  }
  return null
}

/**
 * Pi `agent_end` → result_divider model, read from the last assistant message's
 * `stopReason`/`errorMessage`. Null when the message isn't an `agent_end`.
 *
 * Pi's envelope carries no duration of its own, so the worker measures the turn
 * and injects `duration_ms` — the same field name Claude Code emits, read the
 * same way. `pickNumber` returns null for an absent field, which is what tells
 * an unmeasured turn (no time shown) from a real zero ("(0ms)").
 *
 * `willRetry` is Pi's statement that it restarts this run itself, so the
 * divider draws marked `auto-retry`. "auto-retry" is the term the Pi
 * notification renderer already uses for the `auto_retry_start` line that
 * follows.
 *
 * The label is all it drives now. Keeping the thinking indicator up for the
 * backoff is the WORKER's job: Pi's provider holds its turn flag open across a
 * retry it drives itself (currentTurnActive = willRetry), so the client needs
 * no say in it.
 *
 * A turn the USER stopped comes first, whatever the frame says. Pi spells one
 * stop two ways: a turn it aborts cleanly carries `stopReason: 'aborted'`, and a
 * turn whose tool was still running carries `stopReason: 'error'` with
 * `This operation was aborted`. The second is the same shape as a genuine
 * failure, so the row read "Turn failed" in the danger color for a stop the
 * reader asked for. LeapMux knows which it is, because it sent the abort, and
 * `completion` is where it records that.
 */
export function piResultDivider(parsed: unknown, completion?: MessageCompletion): DividerIR | null {
  if (!isObject(parsed) || pickString(parsed, 'type') !== PI_EVENT.AgentEnd)
    return null

  if (completion === MessageCompletion.INTERRUPTED)
    return { label: turnEndLabel('interrupted', { durationMs: pickNumber(parsed, 'duration_ms') }) }

  const assistant = lastAssistantMessage(parsed.messages)
  const stopReason = assistant ? pickString(assistant, 'stopReason') : ''
  const errorMessage = assistant ? pickString(assistant, 'errorMessage') : ''

  const durationMs = pickNumber(parsed, 'duration_ms')
  const willRetry = pickBool(parsed, 'willRetry')

  if (stopReason === 'error') {
    return {
      label: turnEndLabel('failed', { durationMs, qualifiers: [willRetry && 'auto-retry'], reason: errorMessage }),
      isError: true,
    }
  }
  // An aborted turn is not an error: the reader asked for it.
  if (stopReason === 'aborted')
    return { label: turnEndLabel('interrupted', { durationMs, qualifiers: [willRetry && 'auto-retry'] }) }
  return {
    label: turnEndLabel('ended', {
      durationMs,
      qualifiers: [stopReason === 'length' && 'length limit', willRetry && 'auto-retry'],
    }),
  }
}
