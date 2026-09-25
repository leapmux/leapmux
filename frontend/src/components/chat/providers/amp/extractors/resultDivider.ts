import type { TurnEnd } from '../../../model/divider'
import { AMP_LINE_TYPE, AMP_RESULT_SUBTYPE } from '~/generated/contracts/amp-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'

/**
 * An Amp `result` row read into the turn-end divider, or null for another row.
 *
 * Amp prints one `result` for each PROCESS, so the worker writes the same shape itself
 * for every turn that ends inside a process. Either way the row states `subtype`
 * `success` or `error_during_execution`, and an error states its reason in `error`.
 * The worker measures the turn and adds `duration_ms`.
 *
 * A turn that the READER stopped comes first, whatever the row says: LeapMux knows it
 * sent the interrupt, and the completion records it. Amp itself reports that stop as
 * an error.
 */
export function ampResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  if (!isObject(parsed) || pickString(parsed, 'type') !== AMP_LINE_TYPE.Result)
    return null
  const durationMs = pickNumber(parsed, 'duration_ms')
  if (completion === MessageCompletion.INTERRUPTED)
    return { label: turnEndLabel('interrupted', { durationMs }) }
  if (parsed.is_error === true || pickString(parsed, 'subtype') === AMP_RESULT_SUBTYPE.ErrorDuringExecution)
    return { label: turnEndLabel('failed', { durationMs, reason: pickString(parsed, 'error') }), isError: true }
  return { label: turnEndLabel('ended', { durationMs }) }
}
