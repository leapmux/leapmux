import type { TurnEnd } from '../../../model/divider'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'

/**
 * A Qoder `result` row read into the turn-end divider, or null for another
 * row.
 *
 * Qoder reports a terminal abort as `subtype:"success"` with `is_error:false`
 * and an empty result, and its `total_cost_usd` is always 0. The worker measures
 * the turn and adds `duration_ms`.
 */
export function qoderResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  if (!isObject(parsed) || pickString(parsed, 'type') !== 'result')
    return null
  const durationMs = pickNumber(parsed, 'duration_ms')
  if (completion === MessageCompletion.INTERRUPTED)
    return { label: turnEndLabel('interrupted', { durationMs }) }
  if (parsed.is_error === true || pickString(parsed, 'subtype') === 'error_during_execution')
    return { label: turnEndLabel('failed', { durationMs }), isError: true }
  return { label: turnEndLabel('ended', { durationMs }) }
}
