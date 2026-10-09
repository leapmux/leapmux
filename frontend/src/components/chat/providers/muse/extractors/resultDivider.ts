import type { TurnEnd } from '../../../model/divider'
import { MUSE_METHOD, MUSE_TURN_OUTCOME } from '~/generated/contracts/muse-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'

export function museResultDivider(payload: unknown): TurnEnd | null {
  if (!isObject(payload) || payload.method !== MUSE_METHOD.TurnCompleted)
    return null
  const params = pickObject(payload, 'params')
  const outcome = pickString(params, 'terminal')
  const durationMs = pickNumber(params, 'durationMs')
  const reason = pickString(pickObject(params, 'error'), 'message') || pickString(params, 'reason')
  if (outcome === MUSE_TURN_OUTCOME.Cancelled)
    return { label: turnEndLabel('interrupted', { durationMs, reason }) }
  if (outcome === MUSE_TURN_OUTCOME.Failed)
    return { label: turnEndLabel('failed', { durationMs, reason }), isError: true }
  return { label: turnEndLabel('ended', { durationMs, reason: outcome === MUSE_TURN_OUTCOME.Completed ? reason : `Unknown Muse outcome: ${outcome}` }) }
}
