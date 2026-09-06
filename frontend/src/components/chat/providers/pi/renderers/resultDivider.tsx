import type { ResultDividerModel } from '../../registry'
import { PI_EVENT } from '~/generated/contracts/pi-protocol'
import { isObject, pickBool, pickNumber, pickString } from '~/lib/jsonPick'
import { formatDuration, joinMetaParts } from '../../../rendererUtils'

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
 */
export function piResultDivider(parsed: unknown): ResultDividerModel | null {
  if (!isObject(parsed) || pickString(parsed, 'type') !== PI_EVENT.AgentEnd)
    return null

  const assistant = lastAssistantMessage(parsed.messages)
  const stopReason = assistant ? pickString(assistant, 'stopReason') : ''
  const errorMessage = assistant ? pickString(assistant, 'errorMessage') : ''

  const durationMs = pickNumber(parsed, 'duration_ms')
  const suffix = durationMs !== null ? ` (${formatDuration(durationMs)})` : ''
  const willRetry = pickBool(parsed, 'willRetry')
  const label = (base: string): string =>
    joinMetaParts([base + suffix, willRetry && 'auto-retry'])

  if (stopReason === 'error') {
    return {
      label: label(errorMessage ? `Turn failed — ${errorMessage}` : 'Turn failed'),
      isError: true,
    }
  }
  if (stopReason === 'aborted')
    return { label: label('Turn aborted'), isError: true }
  if (stopReason === 'length')
    return { label: label('Turn ended (length limit)') }
  return { label: label('Turn ended') }
}
