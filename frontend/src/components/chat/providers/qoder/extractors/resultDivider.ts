import type { TurnEnd } from '../../../model/divider'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickNumber, pickString, stringArray } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'

/**
 * A Qoder `result` row read into the turn-end divider, or null for another
 * row.
 *
 * qodercli 1.1.65 reports a turn that an interrupt stopped as
 * `subtype:"error_during_execution"` with `is_error:true` and
 * `errors:["Operation aborted"]`. A genuine failure has the same subtype and
 * flag, so the worker's completion decides the interrupted label: the worker
 * marks the turn that the user stopped.
 *
 * A failed turn states the CLI's explanation in `errors`, and the divider shows
 * it as the detail block. After a stream failure, `errors` is the only place
 * where the provider's own words reach the transcript.
 *
 * `total_cost_usd` is always 0. The worker measures the turn and adds
 * `duration_ms`.
 */
export function qoderResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  if (!isObject(parsed) || pickString(parsed, 'type') !== 'result')
    return null
  const durationMs = pickNumber(parsed, 'duration_ms')
  if (completion === MessageCompletion.INTERRUPTED)
    return { label: turnEndLabel('interrupted', { durationMs }) }
  if (parsed.is_error === true || pickString(parsed, 'subtype') === 'error_during_execution') {
    // `stringArray`, never a cast: `errors` arrives off the wire, and a value
    // that is not a string must not reach the detail as "[object Object]".
    const detail = stringArray(parsed.errors).join('\n').trim()
    // `detail` must be absent (never '') so the shared renderer skips the block.
    return { label: turnEndLabel('failed', { durationMs }), isError: true, ...(detail ? { detail } : {}) }
  }
  return { label: turnEndLabel('ended', { durationMs }) }
}
