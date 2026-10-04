import type { TurnEnd } from '../../../model/divider'
import { CLINE_EVENT, CLINE_RUN_REASON } from '~/generated/contracts/cline-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'
import { CLINE_FIELD, clineEnvelope } from '../protocol'

/** The words of a run's reason that the outcome states no word for. */
const REASON_QUALIFIERS: ReadonlyMap<string, string> = new Map([
  [CLINE_RUN_REASON.MaxIterations, 'iteration limit'],
  [CLINE_RUN_REASON.MistakeLimit, 'mistake limit'],
])

/** Whether one event ends a run. */
export function clineEndsRun(event: string): boolean {
  return event === CLINE_EVENT.RunCompleted || event === CLINE_EVENT.RunFailed || event === CLINE_EVENT.RunAborted
}

/**
 * The end event of a run, read into the turn-end divider, or null for another row.
 *
 * Cline ends a run with `run.completed`, `run.failed` or `run.aborted`, and each
 * states its `reason`; a failure states its `error`. The worker keeps the result's
 * text and usage, and adds the turn's duration as `duration_ms`. It writes the same
 * shape itself for a turn that ended with no end event.
 *
 * A turn that the READER stopped comes first, whatever the row says: LeapMux knows it
 * sent the abort, and the completion records it.
 */
export function clineResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  const envelope = clineEnvelope(parsed)
  if (!envelope || !clineEndsRun(envelope.event))
    return null
  const durationMs = pickNumber(isObject(parsed) ? parsed : undefined, 'duration_ms')
  const reason = pickString(envelope.payload, CLINE_FIELD.Reason)
  if (completion === MessageCompletion.INTERRUPTED || envelope.event === CLINE_EVENT.RunAborted || reason === CLINE_RUN_REASON.Aborted)
    return { label: turnEndLabel('interrupted', { durationMs }) }
  if (envelope.event === CLINE_EVENT.RunFailed || completion === MessageCompletion.ERROR) {
    const error = pickString(envelope.payload, CLINE_FIELD.Error) || pickString(pickObject(envelope.payload, CLINE_FIELD.Result), CLINE_FIELD.Text)
    return { label: turnEndLabel('failed', { durationMs, qualifiers: [REASON_QUALIFIERS.get(reason)], reason: error }), isError: true }
  }
  return { label: turnEndLabel('ended', { durationMs, qualifiers: [REASON_QUALIFIERS.get(reason)] }) }
}
